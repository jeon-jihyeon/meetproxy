package listen

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlackPayloadOwnership(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"type":"event_callback","event_id":"Ev1","api_app_id":"A1","team_id":"T1",` +
		`"event":{"type":"app_mention","user":"U2","channel":"C1","ts":"1893456000.000001","text":"acme"}}`)
	original := bytes.Clone(raw)
	first, accepted, err := decode(raw, "U1")
	require.NoError(t, err)
	require.True(t, accepted)
	payload := bytes.Clone(first.Payload)
	for i := range raw {
		raw[i] = 'x'
	}
	assert.Equal(t, payload, []byte(first.Payload))
	second, accepted, err := decode(original, "U1")
	require.NoError(t, err)
	require.True(t, accepted)
	first.Payload[0] = 'x'
	assert.Equal(t, payload, []byte(second.Payload))
	assert.Equal(t, byte('{'), original[0])
}

func TestSlackTextSyntax(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		text string
	}{
		{"invalid escape", `"\q"`},
		{"unterminated string", `"acme`},
		{"unescaped control", "\"acme\n\""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := []byte(`{"type":"event_callback","event_id":"Ev1","api_app_id":"A1","team_id":"T1",` +
				`"event":{"type":"app_mention","user":"U2","channel":"C1","ts":"1893456000.000001","text":` +
				tc.text + `}}`)

			event, accepted, err := decode(raw, "U1")

			assert.ErrorIs(t, err, ErrEvent)
			assert.False(t, accepted)
			assert.Empty(t, event)
		})
	}
}

func BenchmarkSlackDecode(b *testing.B) {
	for _, size := range []int{1024, 65536, 1048000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			prefix := `{"type":"event_callback","event_id":"Ev1","api_app_id":"A1","team_id":"T1",` +
				`"event":{"type":"app_mention","user":"U2","channel":"C1","ts":"1893456000.000001","text":"`
			raw := []byte(prefix + strings.Repeat("x", size-len(prefix)-3) + `"}}`)
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			for b.Loop() {
				_, accepted, err := decode(raw, "U1")
				if err != nil || !accepted {
					b.Fatal(err)
				}
			}
		})
	}
}
