package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
)

// JobLayer associates a Layer with a Job.
type JobLayer struct {
	Name string `json:"name",bson:"name"`
}

// JobVolume associates one or more Volumes with a Job.
type JobVolume struct {
	Name string `json:"name",bson:"name"`
}

const (
	// ResultBinary indicates that the client should not attempt to interpret the result payload, but
	// provide it as raw bytes.
	ResultBinary = "binary"

	// ResultPickle indicates that the result contains pickled Python objects.
	ResultPickle = "pickle"

	// StatusWaiting indicates that a job has been submitted, but has not yet entered the queue.
	StatusWaiting = "waiting"

	// StatusQueued indicates that a job has been placed into the execution queue.
	StatusQueued = "queued"

	// StatusProcessing indicates that the job is running.
	StatusProcessing = "processing"

	// StatusDone indicates that the job has completed successfully.
	StatusDone = "done"

	// StatusError indicates that the job threw some kind of exception or otherwise returned a non-zero
	// exit code.
	StatusError = "error"

	// StatusKilled indicates that the user requested that the job be terminated.
	StatusKilled = "killed"

	// StatusStalled indicates that the job has gotten stuck (usually fetching dependencies).
	StatusStalled = "stalled"
)

var (
	validResultType = map[string]bool{ResultBinary: true, ResultPickle: true}

	validStatus = map[string]bool{
		StatusWaiting:    true,
		StatusQueued:     true,
		StatusProcessing: true,
		StatusDone:       true,
		StatusError:      true,
		StatusKilled:     true,
		StatusStalled:    true,
	}

	completedStatus = map[string]bool{
		StatusDone:    true,
		StatusError:   true,
		StatusKilled:  true,
		StatusStalled: true,
	}
)

// Collected contains various metrics about the running job.
type Collected struct {
	CPUTimeUser     uint64 `json:"cputime_user,omitempty"`
	CPUTimeSystem   uint64 `json:"cputime_system,omitempty"`
	MemoryFailCount uint64 `json:"memory_failcnt,omitempty"`
	MemoryMaxUsage  uint64 `json:"memory_max_usage,omitempty"`
}

// Job is a user-submitted compute task to be executed in an appropriate Docker container.
type Job struct {
	Command      string            `json:"cmd",bson:"cmd"`
	Name         *string           `json:"name,omitempty",bson:"name,omitempty"`
	Core         string            `json:"core",bson:"core"`
	Multicore    int               `json:"multicore",bson:"multicore"`
	Restartable  bool              `json:"restartable",bson:"restartable"`
	Tags         map[string]string `json:"tags",bson:"tags"`
	Layers       []JobLayer        `json:"layer",bson:"layer"`
	Volumes      []JobVolume       `json:"vol",bson:"vol"`
	Environment  map[string]string `json:"env",bson:"env"`
	ResultSource string            `json:"result_source",bson:"result_source"`
	ResultType   string            `json:"result_type",bson:"result_type"`
	MaxRuntime   int               `json:"max_runtime",bson:"max_runtime"`
	Stdin        []byte            `json:"stdin",bson:"stdin"`

	Profile   *bool   `json:"profile,omitempty",bson:"profile,omitempty"`
	DependsOn *string `json:"depends_on,omitempty",bson:"depends_on,omitempty"`
}

// Validate ensures that all required fields have non-zero values, and that enum-like fields have
// acceptable values.
func (j Job) Validate() *RhoError {
	// Command is required.
	if j.Command == "" {
		return &RhoError{
			Code:    CodeMissingCommand,
			Message: "All jobs must specify a command to execute.",
			Hint:    `Specify a command to execute as a "cmd" element in your job.`,
		}
	}

	// ResultSource
	if j.ResultSource != "stdout" && !strings.HasPrefix(j.ResultSource, "file:") {
		return &RhoError{
			Code:    CodeInvalidResultSource,
			Message: fmt.Sprintf("Invalid result source [%s]", j.ResultSource),
			Hint:    `The "result_source" must be either "stdout" or "file:{path}".`,
		}
	}

	// ResultType
	if _, ok := validResultType[j.ResultType]; ok {
		accepted := make([]string, 0, len(validResultType))
		for tp := range validResultType {
			accepted = append(accepted, tp)
		}

		return &RhoError{
			Code:    CodeInvalidResultType,
			Message: fmt.Sprintf("Invalid result type [%s]", j.ResultType),
			Hint:    fmt.Sprintf(`The "result_type" must be one of the following: %s`, strings.Join(accepted, ", ")),
		}
	}

	return nil
}

// SubmittedJob is a Job that has already been submitted.
type SubmittedJob struct {
	Job

	CreatedAt  StoredTime `json:"created_at",bson:"created_at"`
	StartedAt  StoredTime `json:"started_at,omitempty",bson:"started_at"`
	FinishedAt StoredTime `json:"finished_at,omitempty",bson:"finished_at"`

	Status        string `json:"status",bson:"status"`
	Result        string `json:"result",bson:"result"`
	ReturnCode    string `json:"return_code",bson:"return_code"`
	Runtime       uint64 `json:"runtime",bson:"runtime"`
	QueueDelay    uint64 `json:"queue_delay",bson:"queue_delay"`
	OverheadDelay uint64 `json:"overhead_delay",bson:"overhead_delay"`
	Stderr        string `json:"stderr",bson:"stderr"`
	Stdout        string `json:"stdout",bson:"stdout"`

	Collected Collected `json:"collected,omitempty",bson:"collected,omitempty"`

	JID     uint64 `json:"-",bson:"_id"`
	Account string `json:"-",bson:"account"`
}

// JobHandler dispatches API calls to /job based on request type.
func JobHandler(c *Context, w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		JobListHandler(c, w, r)
	case "POST":
		JobSubmitHandler(c, w, r)
	default:
		RhoError{
			Code:    "3",
			Message: "Method not supported",
			Hint:    "Use GET or POST against this endpoint.",
			Retry:   false,
		}.Report(http.StatusMethodNotAllowed, w)
	}
}

// JobSubmitHandler enqueues a new job associated with the authenticated account.
func JobSubmitHandler(c *Context, w http.ResponseWriter, r *http.Request) {
	type Request struct {
		Jobs []Job `json:"jobs"`
	}

	type Response struct {
		JIDs []uint64 `json:"jids"`
	}

	account, err := Authenticate(c, w, r)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("Authentication failure.")
		return
	}

	var req Request
	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		log.WithFields(log.Fields{
			"error":   err,
			"account": account.Name,
		}).Error("Unable to parse JSON.")

		RhoError{
			Code:    "5",
			Message: "Unable to parse job payload as JSON.",
			Hint:    "Please supply valid JSON in your request.",
			Retry:   false,
		}.Report(http.StatusBadRequest, w)
		return
	}

	jids := make([]uint64, len(req.Jobs))
	for index, rjob := range req.Jobs {
		job := rjob.Job

		// Interpret the deferred fields.
		if rjob.RawResultSource == "stdout" {
			job.ResultSource = StdoutResult
		} else if strings.HasPrefix(rjob.RawResultSource, "file:") {
			path := rjob.RawResultSource[len("file:") : len(rjob.RawResultSource)-1]
			job.ResultSource = FileResult{Path: path}
		} else {
			log.WithFields(log.Fields{
				"account":       account.Name,
				"result_source": rjob.RawResultSource,
			}).Error("Invalid result_source in a submitted job.")

			RhoError{
				Code:    "6",
				Message: "Invalid result_source.",
				Hint:    `"result_source" must be either "stdout" or "file:{path}".`,
				Retry:   false,
			}.Report(http.StatusBadRequest, w)
			return
		}

		switch rjob.RawResultType {
		case BinaryResult.name:
			job.ResultType = BinaryResult
		case PickleResult.name:
			job.ResultType = PickleResult
		default:
			log.WithFields(log.Fields{
				"account":     account.Name,
				"result_type": rjob.RawResultType,
			}).Error("Invalid result_type in a submitted job.")

			RhoError{
				Code:    "7",
				Message: "Invalid result_type.",
				Hint:    `"result_type" must be either "binary" or "pickle".`,
				Retry:   false,
			}.Report(http.StatusBadRequest, w)
			return
		}

		// Pack the job into a SubmittedJob and store it.
		submitted := SubmittedJob{
			Job:       job,
			CreatedAt: JSONTime(time.Now().UTC()),
			Status:    StatusQueued,
			Account:   account.Name,
		}
		jid, err := c.InsertJob(submitted)
		if err != nil {
			log.WithFields(log.Fields{
				"account": account.Name,
				"error":   err,
			}).Error("Unable to enqueue a submitted job.")

			RhoError{
				Code:    "8",
				Message: "Unable to enqueue your job.",
				Retry:   true,
			}.Report(http.StatusServiceUnavailable, w)
			return
		}

		jids[index] = jid
		log.WithFields(log.Fields{
			"jid":     jid,
			"job":     job,
			"account": account.Name,
		}).Info("Successfully submitted a job.")
	}

	response := Response{JIDs: jids}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// JobListHandler provides updated details about one or more jobs currently submitted to the
// cluster.
func JobListHandler(c *Context, w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, `[]`)
}

// JobKillHandler allows a user to prematurely terminate a running job.
func JobKillHandler(c *Context, w http.ResponseWriter, r *http.Request) {
	//
}

// JobKillAllHandler allows a user to terminate all jobs associated with their account.
func JobKillAllHandler(c *Context, w http.ResponseWriter, r *http.Request) {
	//
}

// JobQueueStatsHandler allows a user to view statistics about the jobs that they have submitted.
func JobQueueStatsHandler(c *Context, w http.ResponseWriter, r *http.Request) {
	//
}
