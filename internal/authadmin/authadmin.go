// Package authadmin is `palbase auth` — configuring the auth of the stack this
// directory is linked to.
//
// IT HOLDS NO LOGIC, and that is the design. Every verb names a path on the
// stack's management surface, sends what it was given and prints what came
// back. The shapes belong to the module that answers; restating them here would
// be a second vocabulary that drifts from the first, and this CLI has been bitten
// by that before.
//
// WHERE THESE USED TO LIVE. `config/auth.json`, in the project's source tree,
// applied on every push. That was a second writer racing the panel, and it lost
// silently: a setting changed in the panel went back to the file's value on the
// next deploy, with nothing reporting it. There is one door now, and both the
// panel and this command knock on it.
package authadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

// REST reaches the linked stack's management surface. An interface rather than a
// concrete client so this package stays off the backend package and testable
// without a stack.
type REST interface {
	Do(ctx context.Context, method, path string, body []byte) (int, []byte, error)
}

// Resolvers carries the lazily-built dependency.
//
// It takes the command because resolving the target ANNOUNCES which stack is
// about to be changed, and that announcement belongs to the verb the person
// typed — not to the moment the tree was built, when nothing has been asked for
// yet.
type Resolvers struct {
	REST func(*cobra.Command) (REST, error)
}

const base = "/v1/management/auth"

// call performs one verb and prints the answer.
//
// The module's own refusal is carried out, not replaced: it knows which field
// was wrong and this does not, so a sentence invented here would hide the only
// useful one. And a refusal EXITS NON-ZERO — a script that read a 400 as a
// change is how somebody believes a setting landed when it did not.
func call(r Resolvers, cmd *cobra.Command, method, path string, body []byte) error {
	rest, err := r.REST(cmd)
	if err != nil {
		return err
	}
	status, raw, err := rest.Do(cmd.Context(), method, path, body)
	if err != nil {
		return err
	}
	if status >= 400 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Description != "" {
			return fmt.Errorf("%s: %s", e.Error, e.Description)
		}
		return fmt.Errorf("the stack answered %d: %s", status, strings.TrimSpace(string(raw)))
	}
	return emit(cmd, raw)
}

// emit prints the module's answer as JSON a script can read.
//
// The same surface serves a panel and a shell pipeline, so the shell's end of it
// has to be parseable rather than prose with a body somewhere inside it. An
// empty answer (a 204) prints nothing at all rather than an invented "ok".
func emit(cmd *cobra.Command, raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}
	var pretty any
	if err := json.Unmarshal([]byte(trimmed), &pretty); err != nil {
		cmd.Println(trimmed)
		return nil
	}
	out, err := json.MarshalIndent(pretty, "", "  ")
	if err != nil {
		return err
	}
	cmd.Println(string(out))
	return nil
}

func readBody(cmd *cobra.Command, inline string) ([]byte, error) {
	if strings.TrimSpace(inline) == "" {
		return nil, fmt.Errorf("nothing to send: pass the JSON body with --json")
	}
	if !json.Valid([]byte(inline)) {
		return nil, fmt.Errorf("--json is not valid JSON")
	}
	return []byte(inline), nil
}

// Cmd builds `palbase auth`.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Configure the auth of the stack this directory is linked to",
		Long: `Everything a panel does to auth, from here.

  palbase auth settings get|set --json '{...}'
  palbase auth providers list
  palbase auth providers enable|disable NAME
  palbase auth providers config set NAME --json '{...}'   (server-side merge)
  palbase auth providers config clear NAME
  palbase auth sessions list
  palbase auth sessions revoke SESSION_ID
  palbase auth sessions revoke-all USER_ID
  palbase auth audit
  palbase auth templates list|get KEY|set KEY --json '{...}'
  palbase auth templates send-test KEY --json '{"to":"you@example.com"}'
  palbase auth mfa get USER_ID
  palbase auth mfa reset USER_ID

These act on the stack IMMEDIATELY — there is nothing to push afterwards. Code
and schema are what a deploy carries; settings are what this changes.`,
	}
	cmd.AddCommand(settingsCmd(r), providersCmd(r), sessionsCmd(r), auditCmd(r), templatesCmd(r), mfaCmd(r))
	return cmd
}

func settingsCmd(r Resolvers) *cobra.Command {
	c := &cobra.Command{Use: "settings", Short: "Password bounds, e-mail confirmation, site and redirect URLs"}
	c.AddCommand(&cobra.Command{
		Use: "get", Short: "Show this project's auth settings", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return call(r, cmd, http.MethodGet, base+"/settings", nil)
		},
	})
	var body string
	set := &cobra.Command{
		Use: "set", Short: "Change this project's auth settings", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			b, err := readBody(cmd, body)
			if err != nil {
				return err
			}
			return call(r, cmd, http.MethodPut, base+"/settings", b)
		},
	}
	set.Flags().StringVar(&body, "json", "", "the settings, as JSON")
	c.AddCommand(set)
	return c
}

func providersCmd(r Resolvers) *cobra.Command {
	c := &cobra.Command{Use: "providers", Short: "The identity providers people can sign in with"}
	c.AddCommand(&cobra.Command{
		Use: "list", Short: "List the providers and whether each is configured", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return call(r, cmd, http.MethodGet, base+"/providers", nil)
		},
	})
	toggle := func(use, short string, on bool) *cobra.Command {
		return &cobra.Command{
			Use: use + " NAME", Short: short, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				b, _ := json.Marshal(map[string]bool{"enabled": on})
				return call(r, cmd, http.MethodPost, base+"/providers/"+args[0], b)
			},
		}
	}
	c.AddCommand(toggle("enable", "Turn a provider on", true))
	c.AddCommand(toggle("disable", "Turn a provider off", false))

	cfg := &cobra.Command{Use: "config", Short: "A provider's OAuth credentials"}
	var body string
	setCfg := &cobra.Command{
		Use: "set NAME", Args: cobra.ExactArgs(1),
		Short: "Store a provider's credentials (server-side merge: a blank field keeps the stored value)",
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := readBody(cmd, body)
			if err != nil {
				return err
			}
			return call(r, cmd, http.MethodPut, base+"/providers/"+args[0]+"/config", b)
		},
	}
	setCfg.Flags().StringVar(&body, "json", "", "the credentials, as JSON")
	cfg.AddCommand(setCfg, &cobra.Command{
		Use: "clear NAME", Short: "Forget a provider's credentials", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return call(r, cmd, http.MethodDelete, base+"/providers/"+args[0]+"/config", nil)
		},
	})
	c.AddCommand(cfg)
	return c
}

func sessionsCmd(r Resolvers) *cobra.Command {
	c := &cobra.Command{Use: "sessions", Short: "The sessions open on this project"}
	c.AddCommand(&cobra.Command{
		Use: "list", Short: "List open sessions", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return call(r, cmd, http.MethodGet, base+"/sessions", nil)
		},
	}, &cobra.Command{
		// One session, because a lost laptop is one session. The other verb is
		// for a compromised account, and conflating them makes the common case
		// cost more than it should.
		Use: "revoke SESSION_ID", Short: "End one session", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return call(r, cmd, http.MethodDelete, base+"/sessions/"+args[0], nil)
		},
	}, &cobra.Command{
		Use: "revoke-all USER_ID", Short: "End every session one person holds", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return call(r, cmd, http.MethodPost, base+"/users/"+args[0]+"/sessions/revoke-all", nil)
		},
	})
	return c
}

func auditCmd(r Resolvers) *cobra.Command {
	var limit int
	var cursor, eventType string
	c := &cobra.Command{
		Use: "audit", Short: "This project's auth audit log", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := ""
			add := func(k, v string) {
				if v == "" {
					return
				}
				sep := "?"
				if q != "" {
					sep = "&"
				}
				q += sep + k + "=" + v
			}
			if limit > 0 {
				add("limit", fmt.Sprint(limit))
			}
			add("cursor", cursor)
			add("event_type", eventType)
			return call(r, cmd, http.MethodGet, base+"/audit"+q, nil)
		},
	}
	c.Flags().IntVar(&limit, "limit", 0, "how many entries")
	c.Flags().StringVar(&cursor, "cursor", "", "continue from a previous page")
	c.Flags().StringVar(&eventType, "event-type", "", "only this event type")
	return c
}

func templatesCmd(r Resolvers) *cobra.Command {
	c := &cobra.Command{Use: "templates", Short: "The e-mails and messages this project sends"}
	c.AddCommand(&cobra.Command{
		Use: "list", Short: "List the templates", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return call(r, cmd, http.MethodGet, base+"/templates", nil)
		},
	}, &cobra.Command{
		Use: "get KEY", Short: "Show one template", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return call(r, cmd, http.MethodGet, base+"/templates/"+args[0], nil)
		},
	})
	var body string
	set := &cobra.Command{
		Use: "set KEY", Short: "Change one template", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := readBody(cmd, body)
			if err != nil {
				return err
			}
			return call(r, cmd, http.MethodPut, base+"/templates/"+args[0], b)
		},
	}
	set.Flags().StringVar(&body, "json", "", "the template, as JSON")
	var testBody string
	send := &cobra.Command{
		// The only way to know a template renders and a provider delivers is to
		// send it. Finding that out here beats finding it out from the first
		// person who signs up.
		Use: "send-test KEY", Short: "Send one template to a real address, once", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := readBody(cmd, testBody)
			if err != nil {
				return err
			}
			return call(r, cmd, http.MethodPost, base+"/templates/"+args[0]+"/send-test", b)
		},
	}
	send.Flags().StringVar(&testBody, "json", "", `where to send it, e.g. {"to":"you@example.com"}`)
	c.AddCommand(set, send)
	return c
}

func mfaCmd(r Resolvers) *cobra.Command {
	c := &cobra.Command{Use: "mfa", Short: "A person's second factor"}
	c.AddCommand(&cobra.Command{
		Use: "get USER_ID", Short: "Show what they have enrolled", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return call(r, cmd, http.MethodGet, base+"/users/"+args[0]+"/mfa", nil)
		},
	}, &cobra.Command{
		// The three-in-the-morning verb: somebody lost the phone that holds their
		// authenticator. Reversible in the sense that matters — they enrol again.
		Use: "reset USER_ID", Short: "Reset their second factor", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return call(r, cmd, http.MethodDelete, base+"/users/"+args[0]+"/mfa", nil)
		},
	})
	return c
}
