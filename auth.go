package main

import (
	"fmt"
	"net/http"

	log "github.com/Sirupsen/logrus"
)

// Account represents a user of the cluster.
type Account struct {
	Name  string `bson:"_id"`
	Admin bool   `bson:"admin"`

	// TotalRuntime tracks the cumulative runtime of all jobs submitted on behalf of this account, in
	// nanoseconds.
	TotalRuntime int64 `bson:"total_runtime"`

	// TotalJobs tracks the number of jobs submitted on behalf of this account.
	TotalJobs int64 `bson:"total_jobs"`
}

// Authenticate reads authentication information from HTTP basic auth and attempts to locate a
// corresponding user account.
func Authenticate(c *Context, w http.ResponseWriter, r *http.Request) (*Account, error) {
	accountName, apiKey, ok := r.BasicAuth()
	if !ok {
		// Credentials not provided.
		err := &APIError{
			Code:    CodeCredentialsMissing,
			Message: "You must authenticate.",
			Hint:    "Try using multyvac.config.set_key(api_key='username', api_secret_key='API key', api_url='') before calling other multyvac methods.",
			Retry:   false,
		}
		err.Report(http.StatusUnauthorized, w)
		return nil, err
	}

	if c.Settings.AdminName != "" && c.Settings.AdminKey != "" {
		if accountName == c.Settings.AdminName && apiKey == c.Settings.AdminKey {
			log.WithFields(log.Fields{
				"account": accountName,
			}).Debug("Administrator authenticated.")

			account, err := c.GetAccount(accountName)
			if err != nil {
				return nil, err
			}

			if !account.Admin {
				if err := c.UpdateAccountAdmin(accountName, true); err != nil {
					return nil, err
				}
				account.Admin = true
			}

			return account, nil
		}
	}

	ok, err := c.AuthService.Validate(accountName, apiKey)
	if err != nil {
		apiErr := &APIError{
			Code:    CodeAuthServiceConnection,
			Message: fmt.Sprintf("Unable to connect to authentication service: %v", err),
			Hint:    "This is most likely an internal networking problem on our end.",
			Retry:   true,
		}
		apiErr.Report(http.StatusInternalServerError, w)
		return nil, apiErr
	}
	if !ok {
		apiErr := &APIError{
			Code:    CodeCredentialsIncorrect,
			Message: fmt.Sprintf("Unable to authenticate account [%s]", accountName),
			Hint:    "Double-check the account name and API key you're providing to multyvac.config.set_key().",
			Retry:   false,
		}
		apiErr.Report(http.StatusUnauthorized, w)
		return nil, apiErr
	}

	// Success! Find or create the Account object in Mongo to return.
	account, err := c.GetAccount(accountName)
	if err != nil {
		apiErr := &APIError{
			Code:    CodeStorageError,
			Message: fmt.Sprintf("Unable to communicate with storage: %v", err),
			Hint:    "There was an internal error communicating with our backend storage.",
			Retry:   true,
		}
		apiErr.Report(http.StatusInternalServerError, w)
		return nil, apiErr
	}

	return account, nil
}
