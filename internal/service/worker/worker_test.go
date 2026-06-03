package worker

import (
	"context"
	"testing"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/taskqueue"
)

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestReportSyncFilters(t *testing.T) {
	t.Run("nested filter object", func(t *testing.T) {
		inc, exc := reportSyncFilters(map[string]any{
			"filter": map[string]any{
				"include": []any{`#type == "track"`},
				"exclude": []any{"debug == true"},
			},
		})
		if !eq(inc, []string{`#type == "track"`}) || !eq(exc, []string{"debug == true"}) {
			t.Errorf("nested filter = inc%v exc%v", inc, exc)
		}
	})
	t.Run("top-level filterInclude/Exclude", func(t *testing.T) {
		inc, exc := reportSyncFilters(map[string]any{
			"filterInclude": []string{"a", "b"},
			"filterExclude": []string{"c"},
		})
		if !eq(inc, []string{"a", "b"}) || !eq(exc, []string{"c"}) {
			t.Errorf("top-level = inc%v exc%v", inc, exc)
		}
	})
	t.Run("empty", func(t *testing.T) {
		inc, exc := reportSyncFilters(map[string]any{})
		if len(inc) != 0 || len(exc) != 0 {
			t.Errorf("empty = inc%v exc%v", inc, exc)
		}
	})
}

func TestToStringSlice(t *testing.T) {
	if got := toStringSlice([]string{"x", "y"}); !eq(got, []string{"x", "y"}) {
		t.Errorf("[]string = %v", got)
	}
	if got := toStringSlice([]any{"x", 1, "y"}); !eq(got, []string{"x", "y"}) {
		t.Errorf("[]any (skips non-strings) = %v", got)
	}
	if got := toStringSlice(nil); got != nil {
		t.Errorf("nil = %v", got)
	}
	if got := toStringSlice(42); got != nil {
		t.Errorf("non-slice = %v", got)
	}
}

func TestOverlayBackfillFilter(t *testing.T) {
	t.Run("nested object", func(t *testing.T) {
		var cfg config.Config
		err := overlayBackfillFilter(map[string]any{
			"backfillFilter": map[string]any{
				"table":   "user",
				"events":  []any{"login"},
				"include": []any{`country == "CN"`},
			},
		}, &cfg)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.BackfillFilter.Table != "user" {
			t.Errorf("table = %q", cfg.BackfillFilter.Table)
		}
		if !eq(cfg.BackfillFilter.Events, []string{"login"}) {
			t.Errorf("events = %v", cfg.BackfillFilter.Events)
		}
		if !eq(cfg.BackfillFilter.Include, []string{`country == "CN"`}) {
			t.Errorf("include = %v", cfg.BackfillFilter.Include)
		}
	})
	t.Run("top-level conveniences override", func(t *testing.T) {
		var cfg config.Config
		err := overlayBackfillFilter(map[string]any{
			"table":         "event",
			"events":        []any{"pay", "refund"},
			"filterInclude": []any{"a"},
			"filterExclude": []any{"b"},
		}, &cfg)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.BackfillFilter.Table != "event" {
			t.Errorf("table = %q", cfg.BackfillFilter.Table)
		}
		if !eq(cfg.BackfillFilter.Events, []string{"pay", "refund"}) {
			t.Errorf("events = %v", cfg.BackfillFilter.Events)
		}
		if !eq(cfg.BackfillFilter.Include, []string{"a"}) || !eq(cfg.BackfillFilter.Exclude, []string{"b"}) {
			t.Errorf("include/exclude = %v / %v", cfg.BackfillFilter.Include, cfg.BackfillFilter.Exclude)
		}
	})
}

func TestDecodePayload(t *testing.T) {
	var bf config.BackfillConfig
	err := decodePayload(map[string]any{
		"projectID":    35,
		"token":        "t",
		"pollInterval": "5s", // duration via decode hook
	}, &bf)
	if err != nil {
		t.Fatal(err)
	}
	if bf.ProjectID != 35 || bf.Token != "t" {
		t.Errorf("decoded = %+v", bf)
	}
	if bf.PollInterval.String() != "5s" {
		t.Errorf("pollInterval = %v", bf.PollInterval)
	}
	// Empty payload is a no-op.
	if err := decodePayload(map[string]any{}, &bf); err != nil {
		t.Fatalf("empty payload: %v", err)
	}
}

func TestExecute_UnknownTaskType(t *testing.T) {
	// execute looks up the handler registry; an empty Service has no handlers,
	// so any type is "unknown" — no Mongo-backed field is touched.
	s := &Service{}
	_, err := s.execute(context.Background(), &taskqueue.Task{Type: taskqueue.TaskType("bogus")})
	if err == nil {
		t.Fatal("expected error for unknown task type")
	}
}

func TestHandlerTypes(t *testing.T) {
	if got := (&reportSyncHandler{}).Type(); got != taskqueue.TaskReportSync {
		t.Errorf("reportSyncHandler.Type() = %q", got)
	}
	if got := (&backfillHandler{}).Type(); got != taskqueue.TaskBackfill {
		t.Errorf("backfillHandler.Type() = %q", got)
	}
	if got := (&sqlHandler{}).Type(); got != taskqueue.TaskSQL {
		t.Errorf("sqlHandler.Type() = %q", got)
	}
}

type fakeHandler struct {
	typ    taskqueue.TaskType
	called bool
}

func (f *fakeHandler) Type() taskqueue.TaskType { return f.typ }
func (f *fakeHandler) Execute(context.Context, *taskqueue.Task) (map[string]any, error) {
	f.called = true
	return map[string]any{"ok": true}, nil
}

func TestExecute_DispatchesToRegisteredHandler(t *testing.T) {
	fh := &fakeHandler{typ: taskqueue.TaskReportSync}
	s := &Service{handlers: map[taskqueue.TaskType]Handler{fh.typ: fh}}
	res, err := s.execute(context.Background(), &taskqueue.Task{Type: taskqueue.TaskReportSync})
	if err != nil {
		t.Fatal(err)
	}
	if !fh.called {
		t.Error("handler was not invoked")
	}
	if res["ok"] != true {
		t.Errorf("result = %v", res)
	}
}
