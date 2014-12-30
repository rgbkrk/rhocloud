package main

import (
	"bytes"
	"fmt"
	"time"

	log "github.com/Sirupsen/logrus"
	docker "github.com/smashwilson/go-dockerclient"
)

// OutputCollector is an io.Writer that accumulates output from a specified stream in an attached
// Docker container and appends it to the appropriate field within a SubmittedJob.
type OutputCollector struct {
	context  *Context
	job      *SubmittedJob
	isStdout bool
}

// DescribeStream returns "stdout" or "stderr" to indicate which stream this collector is consuming.
func (c OutputCollector) DescribeStream() string {
	if c.isStdout {
		return "stdout"
	}
	return "stderr"
}

// Write appends bytes to the selected stream and updates the SubmittedJob.
func (c OutputCollector) Write(p []byte) (int, error) {
	log.WithFields(log.Fields{
		"length": len(p),
		"bytes":  string(p),
		"stream": c.DescribeStream(),
	}).Debug("Received output from a job")

	if c.isStdout {
		c.job.Stdout += string(p)
	} else {
		c.job.Stderr += string(p)
	}

	if err := c.context.UpdateJob(c.job); err != nil {
		return 0, err
	}

	return len(p), nil
}

// Runner is the main entry point for the job runner goroutine.
func Runner(c *Context) {
	var client *docker.Client
	var err error

	if c.DockerTLS {
		client, err = docker.NewTLSClient(c.DockerHost, c.DockerCert, c.DockerKey, c.DockerCACert)
		if err != nil {
			log.WithFields(log.Fields{
				"docker host":    c.DockerHost,
				"docker cert":    c.DockerCert,
				"docker key":     c.DockerKey,
				"docker CA cert": c.DockerCACert,
			}).Fatal("Unable to connect to Docker with TLS.")
			return
		}
	} else {
		client, err = docker.NewClient(c.DockerHost)
		if err != nil {
			log.WithFields(log.Fields{
				"docker host": c.DockerHost,
				"error":       err,
			}).Fatal("Unable to connect to Docker.")
			return
		}
	}

	for {
		select {
		case <-time.After(time.Duration(c.Poll) * time.Millisecond):
			Claim(c, client)
		}
	}
}

// Claim acquires the oldest single pending job and launches a goroutine to execute its command in
// a new container.
func Claim(c *Context, client *docker.Client) {
	job, err := c.ClaimJob()
	if err != nil {
		log.WithFields(log.Fields{"error": err}).Error("Unable to claim a job.")
		return
	}
	if job == nil {
		// Nothing to claim.
		return
	}

	go Execute(c, client, job)
}

// Execute launches a container to process the submitted job. It passes any provided stdin data
// to the container and consumes stdout and stderr, updating Mongo as it runs. Once completed, it
// acquires the job's result from its configured source and marks the job as finished.
func Execute(c *Context, client *docker.Client, job *SubmittedJob) {
	defaultFields := log.Fields{
		"jid":     job.JID,
		"account": job.Account,
	}

	reportErr := func(message string, err error) {
		fs := log.Fields{}
		for k, v := range defaultFields {
			fs[k] = v
		}
		fs["err"] = err
		log.WithFields(fs).Error(message)
	}
	checkErr := func(message string, err error) bool {
		if err == nil {
			log.WithFields(defaultFields).Debugf("%s: ok", message)
			return false
		}

		reportErr(fmt.Sprintf("%s: ERROR", message), err)
		return true
	}

	log.WithFields(defaultFields).Info("Launching a job.")

	job.StartedAt = StoreTime(time.Now())
	if err := c.UpdateJob(job); err != nil {
		reportErr("Unable to update the job's start timestamp.", err)
	}

	container, err := client.CreateContainer(docker.CreateContainerOptions{
		Name: job.ContainerName(),
		Config: &docker.Config{
			Image:     c.Image,
			Cmd:       []string{"/bin/bash", "-c", job.Command},
			OpenStdin: true,
			StdinOnce: true,
		},
	})
	if checkErr("Created the job's container", err) {
		return
	}

	// Include container information in this job's logging messages.
	defaultFields["container id"] = container.ID
	defaultFields["container name"] = container.Name

	// Prepare the input and output streams.
	stdin := bytes.NewReader(job.Stdin)
	stdout := OutputCollector{
		context:  c,
		job:      job,
		isStdout: true,
	}
	stderr := OutputCollector{
		context:  c,
		job:      job,
		isStdout: false,
	}

	go func() {
		err = client.AttachToContainer(docker.AttachToContainerOptions{
			Container:    container.ID,
			Stream:       true,
			InputStream:  stdin,
			OutputStream: stdout,
			ErrorStream:  stderr,
			Stdin:        true,
			Stdout:       true,
			Stderr:       true,
		})
		checkErr("Attached to the container", err)
	}()

	// Start the created container.
	err = client.StartContainer(container.ID, &docker.HostConfig{})
	if checkErr("Started the container", err) {
		return
	}

	status, err := client.WaitContainer(container.ID)
	if checkErr("Waited for the container to complete", err) {
		return
	}

	err = client.RemoveContainer(docker.RemoveContainerOptions{ID: container.ID})
	checkErr("Removed the container", err)

	job.FinishedAt = StoreTime(time.Now())
	if status == 0 {
		// Successful termination.
		job.Status = StatusDone
	} else {
		// Something went wrong.
		job.Status = StatusError
	}

	err = c.UpdateJob(job)
	if checkErr("Updated the job's status", err) {
		return
	}

	log.WithFields(log.Fields{"jid": job.JID}).Info("Job complete.")
}
