package hosted

import (
	"context"
	"net/http"
)

func (c *Client) WithBearer(ctx context.Context, attempt func(ctx context.Context, bearer string) (status int, body []byte, err error)) error {
	cred, err := c.credential()
	if err != nil {
		return err
	}
	code, _, err := attempt(ctx, cred.AccessToken)
	if err != nil || code != http.StatusUnauthorized {
		return err
	}
	if cred.IsSession() {
		return ErrSessionTokenRefused
	}
	if cred.IsNode() {
		return ErrNodeTokenRefused
	}
	rotated, err := c.refreshAndPersist(ctx, cred)
	if err != nil {
		return err
	}
	_, _, err = attempt(ctx, rotated.AccessToken)
	return err
}
