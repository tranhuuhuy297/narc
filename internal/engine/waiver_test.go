package engine

import (
	"reflect"
	"testing"
)

func TestParseWaiver(t *testing.T) {
	cases := []struct {
		in     string
		rules  []string
		reason string
	}{
		{`narc:allow skip-added reason="flaky upstream, see #12"`, []string{"skip-added"}, "flaky upstream, see #12"},
		{`narc:allow skip-added,any-cast reason='x y z'`, []string{"skip-added", "any-cast"}, "x y z"},
		{`narc:allow skip-added: upstream is down`, []string{"skip-added"}, "upstream is down"},
		{`narc:allow skip-added -- upstream is down`, []string{"skip-added"}, "upstream is down"},
		{`narc:allow skip-added`, []string{"skip-added"}, ""},
		{`narc:allow reason="everything"`, []string{"*"}, "everything"},
		{`TODO narc:allow test-deleted reason="obsolete endpoint removed in #9"`, []string{"test-deleted"}, "obsolete endpoint removed in #9"},
	}
	for _, c := range cases {
		w, ok := parseWaiver(c.in)
		if !ok {
			t.Errorf("%q: no waiver", c.in)
			continue
		}
		if !reflect.DeepEqual(w.rules, c.rules) || w.reason != c.reason {
			t.Errorf("%q: got rules=%q reason=%q", c.in, w.rules, w.reason)
		}
	}
}
