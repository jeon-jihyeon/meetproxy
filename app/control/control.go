package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"

	model "github.com/jeon-jihyeon/meetproxy/internal/control"
)

const maxArguments = 1 << 20

var (
	ErrInput   = model.ErrInput
	ErrDriver  = errors.New("control driver is unavailable")
	ErrJournal = errors.New("effectful control requires an intent journal")
	ErrOutput  = errors.New("driver returned invalid JSON")
	ErrReceipt = errors.New("invalid recorded control result")
)

type Request struct {
	ID        string          `json:"id,omitempty"`
	Session   string          `json:"session"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

type Result struct {
	ID       string          `json:"id,omitempty"`
	Tool     string          `json:"tool"`
	State    string          `json:"state"`
	Output   json.RawMessage `json:"output,omitempty"`
	Recorded bool            `json:"recorded"`
}

type Driver interface {
	Call(context.Context, string, []byte) ([]byte, bool, error)
}

type Journal interface {
	Claim(context.Context, string, string, []byte) ([]byte, bool, error)
	Save(context.Context, string, string, []byte) error
	Read(context.Context, string) ([]byte, bool, error)
}

type Controller struct {
	driver  Driver
	journal Journal
}

func New(driver Driver, journal Journal) *Controller {
	if absent(driver) {
		driver = nil
	}
	if absent(journal) {
		journal = nil
	}
	return &Controller{driver: driver, journal: journal}
}

func absent(value any) bool {
	if value == nil {
		return true
	}
	switch reflect.TypeOf(value).Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	default:
		return false
	}
}

func Fingerprint(in Request) (string, error) {
	if _, err := model.New(in.ID, in.Tool, in.Session); err != nil {
		return "", err
	}
	_, digest, err := prepare(in)
	return digest, err
}

func (c *Controller) Execute(ctx context.Context, in Request) (Result, error) {
	out := Result{ID: in.ID, Tool: in.Tool, State: string(model.NotStarted)}
	op, err := model.New(in.ID, in.Tool, in.Session)
	if err != nil {
		return out, err
	}
	args, digest, err := prepare(in)
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		if op.Effectful() {
			out.State = string(model.Unknown)
		}
		return out, err
	}
	if op.Effectful() {
		if c.journal == nil {
			return out, ErrJournal
		}
		existing, created, err := c.claimIntent(ctx, op.ID(), digest, out)
		if err != nil {
			return existing, fmt.Errorf("claim control intent: %w", err)
		}
		if !created {
			return existing, nil
		}
	}
	data, started, callErr := c.call(ctx, op.Tool(), args)
	out.State = string(op.Outcome(started, callErr == nil))
	out.Output = data
	if !op.Effectful() {
		return out, callErr
	}
	return c.storeResult(ctx, op.ID(), digest, out, callErr)
}

func (c *Controller) call(ctx context.Context, tool string, args []byte) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if c.driver == nil {
		return nil, false, ErrDriver
	}
	data, started, err := c.driver.Call(ctx, tool, args)
	if len(data) > 16<<20 || !json.Valid(data) {
		if err == nil {
			err = ErrOutput
		}
		return nil, started, err
	}
	return bytes.Clone(data), started, err
}

func (c *Controller) claimIntent(ctx context.Context, id, digest string, pending Result) (Result, bool, error) {
	pending.State = string(model.Unknown)
	initial, err := json.Marshal(pending)
	if err != nil {
		return Result{}, false, err
	}
	stored, created, err := c.journal.Claim(ctx, id, digest, initial)
	if err != nil {
		return pending, created, err
	}
	if created {
		return Result{}, true, nil
	}
	out, err := decodeResult(stored)
	if err == nil && (out.ID != id || out.Tool != pending.Tool) {
		err = fmt.Errorf("%w: identity differs", ErrReceipt)
	}
	if err != nil {
		return pending, false, err
	}
	return out, false, err
}

func (c *Controller) storeResult(ctx context.Context, id, digest string, out Result, callErr error) (Result, error) {
	out.Recorded = true
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(out)
	if err == nil {
		err = c.journal.Save(ctx, id, digest, bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
	}
	if err != nil {
		out.Recorded = false
		return out, errors.Join(callErr, fmt.Errorf("store control result: %w", err))
	}
	return out, callErr
}

func (c *Controller) Lookup(ctx context.Context, id string) (Result, bool, error) {
	if _, err := model.New(id, "lookup", "lookup"); err != nil {
		return Result{}, false, err
	}
	if c.journal == nil {
		return Result{}, false, ErrJournal
	}
	data, exists, err := c.journal.Read(ctx, id)
	if err != nil || !exists {
		return Result{ID: id, State: string(model.Unknown)}, exists, err
	}
	out, err := decodeResult(data)
	if err == nil && out.ID != id {
		err = fmt.Errorf("%w: identity differs", ErrReceipt)
	}
	if err != nil {
		return Result{ID: id, State: string(model.Unknown)}, true, err
	}
	return out, true, err
}

func prepare(in Request) ([]byte, string, error) {
	if len(in.Arguments) > maxArguments {
		return nil, "", ErrInput
	}
	var args map[string]any
	decoder := json.NewDecoder(bytes.NewReader(in.Arguments))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil || args == nil {
		return nil, "", ErrInput
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, "", ErrInput
	}
	if session, exists := args["session"]; exists && session != in.Session {
		return nil, "", ErrInput
	}
	args["session"] = in.Session
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(in.Tool))
	_, _ = digest.Write([]byte("\n"))
	_, _ = digest.Write(encoded)
	return encoded, hex.EncodeToString(digest.Sum(nil)), nil
}

func decodeResult(data []byte) (Result, error) {
	var out Result
	if err := json.Unmarshal(data, &out); err != nil {
		return Result{}, fmt.Errorf("%w: JSON", ErrReceipt)
	}
	if _, err := model.New(out.ID, out.Tool, "receipt"); err != nil || out.ID == "" {
		return Result{}, fmt.Errorf("%w: identity", ErrReceipt)
	}
	if out.State != string(model.Unknown) && !out.Recorded {
		return Result{}, fmt.Errorf("%w: unrecorded outcome", ErrReceipt)
	}
	if out.State == string(model.Completed) && !json.Valid(out.Output) {
		return Result{}, fmt.Errorf("%w: output", ErrReceipt)
	}
	switch model.State(out.State) {
	case model.NotStarted, model.Completed, model.Unknown:
		return out, nil
	default:
		return Result{}, fmt.Errorf("%w: state", ErrReceipt)
	}
}
