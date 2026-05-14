package sso

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/l-lab/cloud-agents/pkg/config"
)

type CheckCodeResponse struct {
	Ticket   string
	UserName string
}

type CheckTicketResponse struct {
	UID        int
	Email      string
	UserNameZH string
	UserName   string
}

type Service struct {
	cfg    config.SSOConfig
	client *http.Client
}

func New(cfg config.SSOConfig) *Service {
	return &Service{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// LoginURL builds the SSO login redirect URL.
// jumpto is the destination the browser should land on after a successful login;
// SSO passes it back unchanged in the callback query string.
func (s *Service) LoginURL(jumpto string) string {
	if jumpto == "" {
		jumpto = "/"
	}
	return fmt.Sprintf("%s/auth/sso/login?app_id=%s&jumpto=%s&version=1.0",
		s.cfg.BaseURL, url.QueryEscape(s.cfg.AppID), url.QueryEscape(jumpto))
}

// CheckCode exchanges the one-time SSO code for a ticket and username.
// POST application/x-www-form-urlencoded — code is valid for 30 s and is single-use.
func (s *Service) CheckCode(ctx context.Context, code string) (*CheckCodeResponse, error) {
	endpoint := s.cfg.BaseURL + "/auth/sso/api/check_code"
	form := url.Values{
		"code":    {code},
		"app_id":  {s.cfg.AppID},
		"app_key": {s.cfg.AppKey},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sso check_code: HTTP %d", resp.StatusCode)
	}

	var envelope struct {
		Errno  int    `json:"errno"`
		ErrMsg string `json:"errmsg"`
		Data   struct {
			Ticket   string `json:"ticket"`
			Username string `json:"username"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, err
	}
	if envelope.Errno != 0 {
		return nil, fmt.Errorf("sso check_code: errno=%d msg=%s", envelope.Errno, envelope.ErrMsg)
	}
	return &CheckCodeResponse{
		Ticket:   envelope.Data.Ticket,
		UserName: envelope.Data.Username,
	}, nil
}

// CheckUserTicket fetches the full user record for a given ticket.
// POST application/x-www-form-urlencoded.
func (s *Service) CheckUserTicket(ctx context.Context, ticket string) (*CheckTicketResponse, error) {
	endpoint := s.cfg.BaseURL + "/auth/sso/api/check_user_ticket"
	form := url.Values{
		"ticket": {ticket},
		"app_id": {s.cfg.AppID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sso check_user_ticket: HTTP %d", resp.StatusCode)
	}

	var envelope struct {
		Errno  int    `json:"errno"`
		ErrMsg string `json:"errmsg"`
		Data   struct {
			UID        int    `json:"uid"`
			Email      string `json:"email"`
			UsernameZH string `json:"username_zh"`
			Username   string `json:"username"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, err
	}
	if envelope.Errno != 0 {
		return nil, fmt.Errorf("sso check_user_ticket: errno=%d msg=%s", envelope.Errno, envelope.ErrMsg)
	}
	return &CheckTicketResponse{
		UID:        envelope.Data.UID,
		Email:      envelope.Data.Email,
		UserNameZH: envelope.Data.UsernameZH,
		UserName:   envelope.Data.Username,
	}, nil
}
