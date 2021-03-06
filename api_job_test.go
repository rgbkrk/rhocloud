package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// JobStorage is a fake Storage implementation that only provides job-relevant storage methods.
type JobStorage struct {
	NullStorage

	Submitted SubmittedJob
	Query     JobQuery
}

func (storage *JobStorage) InsertJob(job SubmittedJob) (uint64, error) {
	storage.Submitted = job

	return 42, nil
}

func (storage *JobStorage) ListJobs(query JobQuery) ([]SubmittedJob, error) {
	storage.Query = query

	j0 := SubmittedJob{
		Job: Job{Command: `echo "1"`},
		JID: 11,
	}
	j1 := SubmittedJob{
		Job: Job{Command: `echo "2"`},
		JID: 22,
	}
	j2 := SubmittedJob{
		Job: Job{Command: `echo "3"`},
		JID: 33,
	}

	results := make([]SubmittedJob, 0, 3)
	for _, job := range []SubmittedJob{j0, j1, j2} {
		if len(query.JIDs) > 0 {
			for _, jid := range query.JIDs {
				if job.JID == jid {
					results = append(results, job)
				}
			}
		} else {
			results = append(results, job)
		}
	}
	return results, nil
}

func (storage *JobStorage) UpdateJob(job *SubmittedJob) error {
	storage.Submitted = *job
	return nil
}

func TestJobHandlerBadRequest(t *testing.T) {
	r, err := http.NewRequest("PUT", "https://localhost/v1/jobs", nil)
	if err != nil {
		t.Fatalf("Unable to create request: %v", err)
	}
	w := httptest.NewRecorder()
	c := &Context{}

	JobHandler(c, w, r)

	hasError(t, w, http.StatusMethodNotAllowed, APIError{
		Code:    CodeMethodNotSupported,
		Message: "Method not supported",
		Retry:   false,
	})
}

func TestSubmitJob(t *testing.T) {
	body := strings.NewReader(`
	{
		"jobs": [{
			"cmd": "id",
			"name": "wat",
			"result_source": "stdout",
			"result_type": "binary"
		}]
	}
	`)
	r, err := http.NewRequest("POST", "https://localhost/v1/jobs", body)
	if err != nil {
		t.Fatalf("Unable to create request: %v", err)
	}
	r.SetBasicAuth("admin", "12345")
	w := httptest.NewRecorder()
	s := &JobStorage{}
	c := &Context{
		Settings: Settings{
			AdminName: "admin",
			AdminKey:  "12345",
		},
		Storage: s,
	}

	JobHandler(c, w, r)

	if w.Code != http.StatusOK {
		t.Errorf("Unexpected HTTP status: [%d]", w.Code)
	}

	var response struct {
		JIDs []uint `json:"jids"`
	}
	out := w.Body.Bytes()
	if err := json.Unmarshal(out, &response); err != nil {
		t.Fatalf("Unable to parse response body as JSON: [%s]", string(out))
	}
	if len(response.JIDs) != 1 {
		t.Fatalf("Expected one JID, received [%d]", len(response.JIDs))
	}
	if response.JIDs[0] != 42 {
		t.Errorf("Expected to be assigned ID 42, got [%d]", response.JIDs[0])
	}

	if s.Submitted.Account != "admin" {
		t.Errorf("Expected submitted job to belong to admin, not [%s]", s.Submitted.Account)
	}
	if s.Submitted.Status != StatusQueued {
		t.Errorf("Expected submitted job to be in state queued, not [%s]", s.Submitted.Status)
	}

	if s.Submitted.CreatedAt == 0 {
		t.Error("Expected the job's CreatedAt time to be populated.")
	}
	if s.Submitted.StartedAt != 0 {
		t.Errorf("Expected the job's StartedAt time to be zero, but was [%s]", s.Submitted.StartedAt)
	}
	if s.Submitted.FinishedAt != 0 {
		t.Errorf("Expected the job's FinishedAt time to be zero, but was [%s]", s.Submitted.FinishedAt)
	}
}

func TestSubmitJobBadResultSource(t *testing.T) {
	body := strings.NewReader(`
	{
		"jobs": [{
			"cmd": "id",
			"name": "wat",
			"result_source": "magic",
			"result_type": "binary"
		}]
	}
	`)
	r, err := http.NewRequest("POST", "https://localhost/v1/jobs", body)
	if err != nil {
		t.Fatalf("Unable to create request: %v", err)
	}
	r.SetBasicAuth("admin", "12345")
	w := httptest.NewRecorder()
	c := &Context{
		Settings: Settings{
			AdminName: "admin",
			AdminKey:  "12345",
		},
		Storage: &JobStorage{},
	}

	JobHandler(c, w, r)

	hasError(t, w, http.StatusBadRequest, APIError{
		Code:    CodeInvalidResultSource,
		Message: "Invalid result source [magic]",
		Retry:   false,
	})
}

func TestSubmitJobBadResultType(t *testing.T) {
	body := strings.NewReader(`
	{
		"jobs": [{
			"cmd": "id",
			"name": "wat",
			"result_source": "stdout",
			"result_type": "elsewhere"
		}]
	}
	`)
	r, err := http.NewRequest("POST", "https://localhost/v1/jobs", body)
	if err != nil {
		t.Fatalf("Unable to create request: %v", err)
	}
	r.SetBasicAuth("admin", "12345")
	w := httptest.NewRecorder()
	c := &Context{
		Settings: Settings{
			AdminName: "admin",
			AdminKey:  "12345",
		},
		Storage: &JobStorage{},
	}

	JobHandler(c, w, r)

	hasError(t, w, http.StatusBadRequest, APIError{
		Code:    CodeInvalidResultType,
		Message: "Invalid result type [elsewhere]",
		Retry:   false,
	})
}

func TestListJobsAll(t *testing.T) {
	r, err := http.NewRequest("GET", "https://localhost/v1/jobs", nil)
	if err != nil {
		t.Fatalf("Unable to create request: %v", err)
	}
	r.SetBasicAuth("admin", "12345")
	w := httptest.NewRecorder()
	s := &JobStorage{}
	c := &Context{
		Settings: Settings{
			AdminName: "admin",
			AdminKey:  "12345",
		},
		Storage: s,
	}

	JobHandler(c, w, r)

	if w.Code != http.StatusOK {
		t.Errorf("Unexpected HTTP status: [%d]", w.Code)
	}

	var response struct {
		Jobs []SubmittedJob `json:"jobs"`
	}
	out := w.Body.Bytes()
	if err := json.Unmarshal(out, &response); err != nil {
		t.Fatalf("Unable to parse response body as JSON: [%s]", string(out))
	}
	t.Logf("Response body:\n%s", out)

	if len(response.Jobs) != 3 {
		t.Fatalf("Unexpected number of jobs returned: [%d]", len(response.Jobs))
	}
	if cmd0 := response.Jobs[0].Command; cmd0 != `echo "1"` {
		t.Errorf(`Expected first job to have command 'echo "1"', had [%s]`, cmd0)
	}
	if cmd1 := response.Jobs[1].Command; cmd1 != `echo "2"` {
		t.Errorf(`Expected second job to have command 'echo "2"', had [%s]`, cmd1)
	}
	if cmd2 := response.Jobs[2].Command; cmd2 != `echo "3"` {
		t.Errorf(`Expected third job to have command 'echo "3"', had [%s]`, cmd2)
	}
}

func jobListQuery(t *testing.T, url string) JobQuery {
	r, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("Unable to create request: %v", err)
	}
	r.SetBasicAuth("admin", "12345")
	w := httptest.NewRecorder()
	s := &JobStorage{}
	c := &Context{
		Settings: Settings{
			AdminName: "admin",
			AdminKey:  "12345",
		},
		Storage: s,
	}

	JobHandler(c, w, r)

	return s.Query
}

func TestListJobsBySingleID(t *testing.T) {
	q := jobListQuery(t, "https://localhost/v1/jobs?jid=123")

	if len(q.JIDs) != 1 {
		t.Errorf("Expected a single JID, got [%v]", q.JIDs)
	}
	if q.JIDs[0] != 123 {
		t.Errorf("Expected JID to be 123, got [%d]", q.JIDs[0])
	}

	if q.Limit != 1000 {
		t.Errorf("Expected limit to default to 1000, got [%d]", q.Limit)
	}
}

func TestListJobsByMultipleIDs(t *testing.T) {
	q := jobListQuery(t, "https://localhost/v1/jobs?jid=123&jid=456&jid=789")

	if len(q.JIDs) != 3 {
		t.Errorf("Expected three JIDs, got [%v]", q.JIDs)
	}
	for i, expected := range []uint64{123, 456, 789} {
		if q.JIDs[i] != expected {
			t.Errorf("Expected [%d] for element %d, got [%d]", expected, i, q.JIDs[i])
		}
	}
}

func TestListJobsBySingleName(t *testing.T) {
	q := jobListQuery(t, "https://localhost/v1/jobs?name=foo")

	if len(q.Names) != 1 {
		t.Errorf("Expected a single name, got [%v]", q.Names)
	}
	if q.Names[0] != "foo" {
		t.Errorf("Expected JID to be foo, got [%s]", q.Names[0])
	}
}

func TestListJobsByMultipleNames(t *testing.T) {
	q := jobListQuery(t, "https://localhost/v1/jobs?name=foo&name=bar")

	if len(q.Names) != 2 {
		t.Errorf("Expected two names, got [%v]", q.Names)
	}
	for i, expected := range []string{"foo", "bar"} {
		if q.Names[i] != expected {
			t.Errorf("Expected name %d to be [%s], got [%s]", i, expected, q.Names[i])
		}
	}
}

func TestListJobsMaximumLimit(t *testing.T) {
	q := jobListQuery(t, "https://localhost/v1/jobs?name=foo&limit=99999999")

	if q.Limit != 9999 {
		t.Errorf("Expected handler to clamp limit to 9999, but was %d", q.Limit)
	}
}

func TestSubmittedJobContainerName(t *testing.T) {
	name := "wat"
	explicitName := SubmittedJob{
		Job: Job{Name: &name},
		JID: 1234,
	}
	if containerName := explicitName.ContainerName(); containerName != "job_1234_wat" {
		t.Errorf("Expected explicit name to be [job_1234_wat], was [%s]", containerName)
	}

	anonymous := SubmittedJob{JID: 4321}
	if containerName := anonymous.ContainerName(); containerName != "job_4321_unnamed" {
		t.Errorf("Expected anonymous name to be [job_4321_unnamed], was [%s]", containerName)
	}
}

func TestSubmitJobKill(t *testing.T) {
	r, err := http.NewRequest("POST", "https://localhost/v1/jobs/kill", strings.NewReader("jid=11"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err != nil {
		t.Fatalf("Unable to create request: %v", err)
	}
	r.SetBasicAuth("admin", "12345")
	w := httptest.NewRecorder()
	s := &JobStorage{}
	c := &Context{
		Settings: Settings{
			AdminName: "admin",
			AdminKey:  "12345",
		},
		Storage: s,
	}

	JobKillHandler(c, w, r)

	if !s.Submitted.KillRequested {
		t.Error("Expected a job kill to be requested")
	}
}
