package approval

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Status string

const (
	StatusPending  Status = "approval_pending"
	StatusGranted  Status = "approval_granted"
	StatusConsumed Status = "approval_consumed"
	StatusExpired  Status = "approval_expired"
	StatusRevoked  Status = "approval_revoked"
)

type Scope struct {
	Policy     string `json:"policy"`
	Action     string `json:"action"`
	Repository string `json:"repository,omitempty"`
	Branch     string `json:"branch,omitempty"`
}

type DelegationHop struct {
	Actor     string `json:"actor"`
	SessionID string `json:"session_id,omitempty"`
	Reason    string `json:"reason"`
}

type Grant struct {
	Token              string          `json:"token"`
	Scope              Scope           `json:"scope"`
	ApprovingActor     string          `json:"approving_actor"`
	ApprovedExecutor   string          `json:"approved_executor"`
	Status             Status          `json:"status"`
	ExpiresAt          *time.Time      `json:"expires_at,omitempty"`
	MaxUses            int             `json:"max_uses"`
	Uses               int             `json:"uses"`
	DelegationChain    []DelegationHop `json:"delegation_chain,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	LastAuditErrorKind string          `json:"last_audit_error_kind,omitempty"`
}

type Audit struct {
	Kind               string          `json:"kind"`
	Token              string          `json:"token"`
	Scope              Scope           `json:"scope"`
	ApprovingActor     string          `json:"approving_actor"`
	ExecutingActor     string          `json:"executing_actor"`
	Status             Status          `json:"status"`
	DelegatedExecution bool            `json:"delegated_execution"`
	DelegationChain    []DelegationHop `json:"delegation_chain"`
	Uses               int             `json:"uses"`
	MaxUses            int             `json:"max_uses"`
	VerifiedAt         time.Time       `json:"verified_at"`
}

type Error struct {
	Kind     string `json:"kind"`
	Message  string `json:"message"`
	Expected any    `json:"expected,omitempty"`
	Actual   any    `json:"actual,omitempty"`
}

func (e Error) Error() string {
	if e.Message != "" {
		return e.Kind + ": " + e.Message
	}
	return e.Kind
}

type Ledger struct {
	Kind   string  `json:"kind"`
	Grants []Grant `json:"grants"`
}

type Store struct {
	ConfigHome string
}

type GrantOptions struct {
	Token            string
	Scope            Scope
	ApprovingActor   string
	ApprovedExecutor string
	Status           Status
	ExpiresAt        *time.Time
	MaxUses          int
	DelegationChain  []DelegationHop
	Now              time.Time
}

func NewStore(configHome string) Store {
	return Store{ConfigHome: configHome}
}

func (s Store) Grant(opts GrantOptions) (Grant, error) {
	if err := validateScope(opts.Scope); err != nil {
		return Grant{}, err
	}
	approvingActor := strings.TrimSpace(opts.ApprovingActor)
	if approvingActor == "" {
		return Grant{}, errors.New("approving_actor is required")
	}
	approvedExecutor := strings.TrimSpace(opts.ApprovedExecutor)
	if approvedExecutor == "" {
		return Grant{}, errors.New("approved_executor is required")
	}
	status := opts.Status
	if status == "" {
		status = StatusGranted
	}
	if !validStatus(status) {
		return Grant{}, fmt.Errorf("unknown approval status %q", status)
	}
	token := strings.TrimSpace(opts.Token)
	if token == "" {
		generated, err := GenerateToken()
		if err != nil {
			return Grant{}, err
		}
		token = generated
	}
	now := opts.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	maxUses := opts.MaxUses
	if maxUses <= 0 {
		maxUses = 1
	}
	ledger, err := s.load()
	if err != nil {
		return Grant{}, err
	}
	if _, ok := ledger[token]; ok {
		return Grant{}, fmt.Errorf("approval token %q already exists", token)
	}
	grant := Grant{
		Token:            token,
		Scope:            normalizeScope(opts.Scope),
		ApprovingActor:   approvingActor,
		ApprovedExecutor: approvedExecutor,
		Status:           status,
		ExpiresAt:        normalizeExpiry(opts.ExpiresAt),
		MaxUses:          maxUses,
		DelegationChain:  normalizeDelegation(opts.DelegationChain),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	ledger[token] = grant
	return grant, s.save(ledger)
}

func (s Store) Verify(token string, scope Scope, executingActor string, now time.Time) (Audit, error) {
	ledger, err := s.load()
	if err != nil {
		return Audit{}, err
	}
	grant, ok := ledger[strings.TrimSpace(token)]
	if !ok {
		return Audit{}, tokenError("no_approval", "approval token was not found", nil, token)
	}
	if err := validateGrant(grant, scope, executingActor, now); err != nil {
		return Audit{}, err
	}
	return auditFor(grant, executingActor, normalizedNow(now)), nil
}

func (s Store) Consume(token string, scope Scope, executingActor string, now time.Time) (Audit, error) {
	ledger, err := s.load()
	if err != nil {
		return Audit{}, err
	}
	token = strings.TrimSpace(token)
	grant, ok := ledger[token]
	if !ok {
		return Audit{}, tokenError("no_approval", "approval token was not found", nil, token)
	}
	if err := validateGrant(grant, scope, executingActor, now); err != nil {
		grant.LastAuditErrorKind = errorKind(err)
		grant.UpdatedAt = normalizedNow(now)
		ledger[token] = grant
		_ = s.save(ledger)
		return Audit{}, err
	}
	grant.Uses++
	if grant.Uses >= grant.MaxUses {
		grant.Status = StatusConsumed
	}
	grant.UpdatedAt = normalizedNow(now)
	ledger[token] = grant
	if err := s.save(ledger); err != nil {
		return Audit{}, err
	}
	return auditFor(grant, executingActor, grant.UpdatedAt), nil
}

func (s Store) Revoke(token string, now time.Time) (Audit, error) {
	ledger, err := s.load()
	if err != nil {
		return Audit{}, err
	}
	token = strings.TrimSpace(token)
	grant, ok := ledger[token]
	if !ok {
		return Audit{}, tokenError("no_approval", "approval token was not found", nil, token)
	}
	grant.Status = StatusRevoked
	grant.UpdatedAt = normalizedNow(now)
	ledger[token] = grant
	if err := s.save(ledger); err != nil {
		return Audit{}, err
	}
	return auditFor(grant, grant.ApprovedExecutor, grant.UpdatedAt), nil
}

func (s Store) List() (Ledger, error) {
	ledger, err := s.load()
	if err != nil {
		return Ledger{}, err
	}
	grants := make([]Grant, 0, len(ledger))
	for _, grant := range ledger {
		grants = append(grants, grant)
	}
	sort.Slice(grants, func(i, j int) bool {
		return grants[i].CreatedAt.Before(grants[j].CreatedAt)
	})
	return Ledger{Kind: "approval_token_ledger", Grants: grants}, nil
}

func GenerateToken() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "codog-approval-" + hex.EncodeToString(bytes[:]), nil
}

func validateGrant(grant Grant, scope Scope, executingActor string, now time.Time) error {
	if grant.Status == StatusPending {
		return tokenError("approval_pending", "approval token is pending", StatusGranted, grant.Status)
	}
	if grant.Status == StatusConsumed {
		return tokenError("approval_already_consumed", "approval token was already consumed", StatusGranted, grant.Status)
	}
	if grant.Status == StatusExpired {
		return tokenError("approval_expired", "approval token is expired", StatusGranted, grant.Status)
	}
	if grant.Status == StatusRevoked {
		return tokenError("approval_revoked", "approval token was revoked", StatusGranted, grant.Status)
	}
	if grant.Status != StatusGranted {
		return tokenError("approval_invalid_status", "approval token has an invalid status", StatusGranted, grant.Status)
	}
	now = normalizedNow(now)
	if grant.ExpiresAt != nil && now.After(*grant.ExpiresAt) {
		return tokenError("approval_expired", "approval token is expired", grant.ExpiresAt, now)
	}
	if grant.Uses >= grant.MaxUses {
		return tokenError("approval_already_consumed", "approval token usage limit is exhausted", grant.MaxUses, grant.Uses)
	}
	normalizedScope := normalizeScope(scope)
	if normalizeScope(grant.Scope) != normalizedScope {
		return tokenError("approval_scope_mismatch", "approval token scope does not match requested action", grant.Scope, normalizedScope)
	}
	executingActor = strings.TrimSpace(executingActor)
	if executingActor == "" {
		return errors.New("executing_actor is required")
	}
	if grant.ApprovedExecutor != executingActor {
		return tokenError("approval_unauthorized_delegate", "approval token is not delegated to this executor", grant.ApprovedExecutor, executingActor)
	}
	return nil
}

func auditFor(grant Grant, executingActor string, now time.Time) Audit {
	chain := append([]DelegationHop(nil), grant.DelegationChain...)
	if len(chain) == 0 {
		chain = append(chain, DelegationHop{Actor: grant.ApprovingActor, Reason: "approval granted"})
	}
	if grant.ApprovingActor != executingActor && !delegationContains(chain, executingActor) {
		chain = append(chain, DelegationHop{Actor: executingActor, Reason: "delegated execution"})
	}
	return Audit{
		Kind:               "approval_token_audit",
		Token:              grant.Token,
		Scope:              grant.Scope,
		ApprovingActor:     grant.ApprovingActor,
		ExecutingActor:     executingActor,
		Status:             grant.Status,
		DelegatedExecution: grant.ApprovingActor != executingActor,
		DelegationChain:    chain,
		Uses:               grant.Uses,
		MaxUses:            grant.MaxUses,
		VerifiedAt:         normalizedNow(now),
	}
}

func delegationContains(chain []DelegationHop, actor string) bool {
	for _, hop := range chain {
		if hop.Actor == actor {
			return true
		}
	}
	return false
}

func validateScope(scope Scope) error {
	scope = normalizeScope(scope)
	if scope.Policy == "" {
		return errors.New("scope.policy is required")
	}
	if scope.Action == "" {
		return errors.New("scope.action is required")
	}
	return nil
}

func normalizeScope(scope Scope) Scope {
	return Scope{
		Policy:     strings.TrimSpace(scope.Policy),
		Action:     strings.TrimSpace(scope.Action),
		Repository: strings.TrimSpace(scope.Repository),
		Branch:     strings.TrimSpace(scope.Branch),
	}
}

func normalizeDelegation(chain []DelegationHop) []DelegationHop {
	out := make([]DelegationHop, 0, len(chain))
	for _, hop := range chain {
		hop.Actor = strings.TrimSpace(hop.Actor)
		hop.SessionID = strings.TrimSpace(hop.SessionID)
		hop.Reason = strings.TrimSpace(hop.Reason)
		if hop.Actor == "" && hop.Reason == "" && hop.SessionID == "" {
			continue
		}
		out = append(out, hop)
	}
	return out
}

func normalizeExpiry(expiresAt *time.Time) *time.Time {
	if expiresAt == nil || expiresAt.IsZero() {
		return nil
	}
	value := expiresAt.UTC()
	return &value
}

func normalizedNow(now time.Time) time.Time {
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return now
}

func validStatus(status Status) bool {
	switch status {
	case StatusPending, StatusGranted, StatusConsumed, StatusExpired, StatusRevoked:
		return true
	default:
		return false
	}
}

func tokenError(kind string, message string, expected any, actual any) Error {
	return Error{Kind: kind, Message: message, Expected: expected, Actual: actual}
}

func errorKind(err error) string {
	var approvalErr Error
	if errors.As(err, &approvalErr) {
		return approvalErr.Kind
	}
	return "approval_error"
}

func (s Store) load() (map[string]Grant, error) {
	path, err := s.path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Grant{}, nil
		}
		return nil, err
	}
	var ledger Ledger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return nil, err
	}
	out := map[string]Grant{}
	for _, grant := range ledger.Grants {
		if strings.TrimSpace(grant.Token) == "" {
			continue
		}
		out[grant.Token] = grant
	}
	return out, nil
}

func (s Store) save(ledger map[string]Grant) error {
	path, err := s.path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	grants := make([]Grant, 0, len(ledger))
	for _, grant := range ledger {
		grants = append(grants, grant)
	}
	sort.Slice(grants, func(i, j int) bool {
		return grants[i].CreatedAt.Before(grants[j].CreatedAt)
	})
	data, err := json.MarshalIndent(Ledger{Kind: "approval_token_ledger", Grants: grants}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".approval-tokens-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (s Store) path() (string, error) {
	if strings.TrimSpace(s.ConfigHome) == "" {
		return "", errors.New("config home is required")
	}
	return filepath.Join(s.ConfigHome, "approval-tokens.json"), nil
}
