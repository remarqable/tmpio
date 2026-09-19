package ai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/remarqable/tmpio/internal/platform/config"
)

func TestClient(t *testing.T) {
	assert.Nil(t, New(config.AI{}), "no key means no client")

	var got map[string]any
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/messages", r.URL.Path)
		assert.Equal(t, "k", r.Header.Get("x-api-key"))
		assert.Equal(t, "2023-06-01", r.Header.Get("anthropic-version"))
		assert.Equal(t, "wrkspc_1", r.Header.Get("anthropic-workspace-id"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{\"directory\":\"/x\"}"}],"usage":{"input_tokens":12,"output_tokens":3}}`))
	}))
	defer srv.Close()

	c := New(config.AI{APIKey: "k", Model: "m", BaseURL: srv.URL, TimeoutSeconds: 2, WorkspaceID: "wrkspc_1"})
	require.NotNil(t, c)
	res, err := c.Complete(t.Context(), "sys", "hello", 50)
	require.NoError(t, err)
	assert.Equal(t, `{"directory":"/x"}`, res.Text)
	assert.Equal(t, 12, res.InputTokens)
	assert.Equal(t, "m", got["model"])
	assert.Equal(t, "sys", got["system"])
	assert.EqualValues(t, 50, got["max_tokens"])

	status = http.StatusServiceUnavailable
	_, err = c.Complete(t.Context(), "sys", "hello", 50)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overloaded_error: busy")
}
