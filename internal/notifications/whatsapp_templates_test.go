package notifications

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWhatsAppTemplateChangesDoNotWriteProviderCredentials(t *testing.T) {
	document := `{"templates":[{"slug":"auth.code","locale":"en","name":"verification_code","language":"en_US","variables":["code"],"code_button":true}]}`
	file := writeSecretIn(t, t.TempDir(), "templates.json", document)
	rest := &fakeStack{answer: `{"applied":1}`}
	_, err := runWith(t, rest, "templates", "set", "--channel", "whatsapp", "--file", file)
	require.NoError(t, err)
	require.Len(t, rest.all(), 1)
	require.Equal(t, http.MethodPut, rest.last().Method)
	require.Equal(t, whatsappTemplatesPath, rest.last().Path)
	require.JSONEq(t, document, rest.last().Body)
	out, err := runWith(t, &fakeStack{answer: document}, "templates", "list", "--channel", "whatsapp")
	require.NoError(t, err)
	require.JSONEq(t, document, out)
}

func TestWhatsAppTemplateFileRejectsConnectionFieldsBeforeWriting(t *testing.T) {
	rest := &fakeStack{}
	file := writeSecretIn(t, t.TempDir(), "invalid.json", `{"templates":[],"access_token":"not-content"}`)
	_, err := runWith(t, rest, "templates", "set", "--channel", "whatsapp", "--file", file)
	require.Error(t, err)
	require.Empty(t, rest.all())
}
