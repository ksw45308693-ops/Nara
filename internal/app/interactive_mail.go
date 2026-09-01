package app

import (
	"context"
	"errors"
	"time"
)

type mailRetryPolicy struct {
	Attempts                     int
	TotalTimeout, AttemptTimeout time.Duration
}

var interactiveMailRetryPolicy = mailRetryPolicy{
	Attempts:       3,
	TotalTimeout:   45 * time.Second,
	AttemptTimeout: 12 * time.Second,
}

// sendMailWithRetry bounds request-driven SMTP work independently from the
// background digest runner. A shorter caller deadline always wins.
func sendMailWithRetry(ctx context.Context, mailer Mailer, from, to string, message []byte, policy mailRetryPolicy) error {
	if mailer == nil {
		return errors.New("mailer is not configured")
	}
	if policy.Attempts < 1 || policy.TotalTimeout <= 0 || policy.AttemptTimeout <= 0 {
		return errors.New("valid mail retry policy is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	overallCtx, cancelOverall := context.WithTimeout(ctx, policy.TotalTimeout)
	defer cancelOverall()

	var sendErr error
	for attempt := 0; attempt < policy.Attempts; attempt++ {
		if err := overallCtx.Err(); err != nil {
			return err
		}
		attemptCtx, cancelAttempt := context.WithTimeout(overallCtx, policy.AttemptTimeout)
		sendErr = mailer.Send(attemptCtx, from, to, message)
		attemptErr := attemptCtx.Err()
		cancelAttempt()
		if sendErr == nil {
			return nil
		}
		if err := overallCtx.Err(); err != nil {
			return err
		}
		if attemptErr != nil {
			sendErr = attemptErr
		}
	}
	return sendErr
}
