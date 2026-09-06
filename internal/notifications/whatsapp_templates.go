package notifications

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

const whatsappTemplatesPath = "/v1/management/notifications/whatsapp/templates"

type whatsappTemplateDefinition struct {
	Slug       string   `json:"slug"`
	Locale     string   `json:"locale"`
	Name       string   `json:"name"`
	Language   string   `json:"language"`
	Variables  []string `json:"variables"`
	CodeButton bool     `json:"code_button"`
	IsDefault  bool     `json:"is_default,omitempty"`
}

type whatsappTemplatesDocument struct {
	Templates []whatsappTemplateDefinition `json:"templates"`
}

func setWhatsAppTemplates(r Resolvers, cmd *cobra.Command, file string) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	if len(raw) > 128*1024 {
		return fmt.Errorf("WhatsApp template document exceeds 128 KiB")
	}
	var document whatsappTemplatesDocument
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("invalid WhatsApp template document: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF || len(document.Templates) == 0 || len(document.Templates) > 100 {
		return fmt.Errorf("provide one document containing 1–100 WhatsApp template definitions")
	}
	for _, definition := range document.Templates {
		if definition.Slug == "" || definition.Name == "" || definition.Language == "" {
			return fmt.Errorf("each definition requires slug, name and language; language identifies the Meta translation")
		}
	}
	body, err := json.Marshal(document)
	if err != nil {
		return err
	}
	answer, err := call(r, cmd, http.MethodPut, whatsappTemplatesPath, body)
	if err != nil {
		return err
	}
	var result struct {
		Applied int `json:"applied"`
	}
	if json.Unmarshal(answer, &result) != nil || result.Applied != len(document.Templates) {
		return fmt.Errorf("the backend did not confirm the complete template update; inspect notifications templates list --channel whatsapp")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "✓ %d WhatsApp template definition(s) saved\n", result.Applied)
	return nil
}

func listWhatsAppTemplates(r Resolvers, cmd *cobra.Command) error {
	raw, err := call(r, cmd, http.MethodGet, whatsappTemplatesPath, nil)
	if err != nil {
		return err
	}
	var document whatsappTemplatesDocument
	if json.Unmarshal(raw, &document) != nil || document.Templates == nil {
		return fmt.Errorf("the backend did not return a WhatsApp template catalog")
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(document)
}
