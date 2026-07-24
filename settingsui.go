package tmdb

import (
	"context"

	"github.com/mosaic-media/contracts/ui"
	v1 "github.com/mosaic-media/sdk/contracts/platform/v1"
)

// SettingsUI renders the module's own settings screen as SDUI (RoleSettingsUI,
// ADR 0038): set or replace the API key, choose a language and region, toggle
// adult results, and read the attribution TMDB's terms require.
//
// This role is not optional decoration for this module the way it is for one
// with a usable default. TMDB has no anonymous access, so **the module does
// nothing at all until a key is set** and this screen is the only path to
// setting one — a capability with no client path is not done, it is owed.
//
// Every mutating control is an Invoke of the Platform's configureModule command
// carrying the complete new settings document, so the Platform stays the one
// that persists them and the module never holds state between invocations. The
// screen is returned as serialised UINode JSON, which is what keeps the SDK
// SDUI-agnostic.
func (c *Capability) SettingsUI(ctx context.Context, req v1.SettingsUIRequest) (v1.SettingsUIResponse, error) {
	s, err := settingsFrom(req.Settings)
	if err != nil {
		return v1.SettingsUIResponse{}, err
	}

	body := []ui.El{
		apiKeySection(s),
		localeSection(s),
		contentSection(s),
		catalogSection(s),
		attributionSection(),
	}
	screen := ui.Screen(ui.Title("TMDB"), ui.Group(body...))

	data, err := screen.BuildJSON()
	if err != nil {
		return v1.SettingsUIResponse{}, err
	}
	return v1.SettingsUIResponse{UI: data}, nil
}

// configureInput builds the configureModule invoke input for a settings value —
// the complete document the Platform persists. Controls mutate a copy and pass
// it here, so a change to one field never silently drops another.
//
// **The whole document, including the API key, is what a control has to carry,
// and that is a gap in ADR 0021 rather than a choice made here.**
// configureModule *replaces* the stored document; there is no partial update. So
// a module with a secret setting must echo that secret back through the client
// on every control that changes anything else, or setting a language would erase
// the key. The consequence is that the credential appears inside the action
// payloads of this screen — reaching only the admin who passed
// `module.configure`, but bypassing the Platform's redaction classes
// (ADR 0056), which cannot see inside a module's opaque settings document.
//
// What the SDK is missing is either a merge semantic on configureModule or a
// write-only settings field. Recorded as a finding rather than worked around:
// the alternative available here — dropping every setting except the key so no
// control ever carries it — buys the property by removing the features, and the
// module is meant to find the gap, not to hide it.
func configureInput(s settings) map[string]any {
	catalogs := make([]any, 0, len(s.Catalogs))
	for _, c := range s.Catalogs {
		catalogs = append(catalogs, map[string]any{"name": c.Name, "type": c.Type, "query": c.Query})
	}
	return map[string]any{
		"moduleId": CapabilityID,
		"settings": map[string]any{
			"apiKey":       s.APIKey,
			"language":     s.Language,
			"region":       s.Region,
			"includeAdult": s.IncludeAdult,
			"catalogs":     catalogs,
		},
	}
}

// apiKeySection is the credential form: the current state of the key, a field to
// set or replace it, and — only when there is one — a control to clear it.
func apiKeySection(s settings) *ui.Element {
	// The typed value substitutes for "$value" anywhere in the action, which is
	// how a text field becomes a configureModule invoke (ADR 0038).
	pending := s
	pending.APIKey = "$value"
	field := ui.Component("SubmitField",
		ui.Prop("placeholder", "Paste your TMDB API key or read access token…"),
		ui.Prop("submitLabel", "Save"),
		ui.OnTap(ui.Invoke("configureModule", configureInput(pending))))

	// Three states, and the middle one is the reason this section is not a
	// one-liner: a user with no key of their own may still have working metadata,
	// and a screen that showed an empty field would read as broken.
	if s.APIKey == "" {
		if defaultReadAccessToken == "" {
			return ui.Section("API key",
				ui.Banner("TMDB has no anonymous access, so metadata and search do nothing until a key is set. Create a free account at themoviedb.org, then copy the API Read Access Token from Settings › API.", ui.ToneWarning),
				field)
		}
		// The bundled token is described, never shown. It is not this user's
		// credential and there is nothing for them to copy, verify or fix — so
		// rendering any part of it would be noise at best.
		return ui.Section("API key",
			ui.Banner("Using the read access token bundled with Mosaic, so metadata works without any setup. Add your own below if you would rather not share its rate limit — yours will take over immediately.", ui.ToneSuccess),
			statusRow(ui.Badge("Bundled key in use", ui.ToneSuccess)),
			field)
	}

	cleared := s
	cleared.APIKey = ""
	revert := "Clearing it stops TMDB working until you add another."
	if defaultReadAccessToken != "" {
		revert = "Clearing it falls back to the key bundled with Mosaic."
	}
	return ui.Section("API key",
		ui.Banner("Your own key is in use. Saving a new one replaces it. "+revert, ui.ToneSuccess),
		statusRow(
			ui.Badge(maskKey(s.APIKey), ui.ToneNeutral),
			ui.Badge(keyKindLabel(s.APIKey), ui.ToneInfo),
			ui.Button("Clear", "danger", ui.OnTap(ui.Invoke("configureModule", configureInput(cleared))))),
		field)
}

// localeSection sets the language TMDB is queried in and the region that decides
// what "in cinemas" means.
func localeSection(s settings) *ui.Element {
	pendingLanguage := s
	pendingLanguage.Language = "$value"
	pendingRegion := s
	pendingRegion.Region = "$value"

	return ui.Section("Language and region",
		labelledRow("Language", s.Language,
			ui.Component("SubmitField",
				ui.Prop("placeholder", "Language tag, e.g. en-US or de-DE"),
				ui.Prop("submitLabel", "Set"),
				ui.OnTap(ui.Invoke("configureModule", configureInput(pendingLanguage))))),
		labelledRow("Region", regionLabel(s.Region),
			ui.Component("SubmitField",
				ui.Prop("placeholder", "Country code, e.g. GB or US"),
				ui.Prop("submitLabel", "Set"),
				ui.OnTap(ui.Invoke("configureModule", configureInput(pendingRegion))))))
}

// contentSection carries the adult-results toggle. It is its own section rather
// than a row in the one above because it changes what search returns, not how it
// is presented.
func contentSection(s settings) *ui.Element {
	toggled := s
	toggled.IncludeAdult = !s.IncludeAdult

	label, tone := "Excluded", ui.ToneNeutral
	action := "Include adult results"
	if s.IncludeAdult {
		label, tone = "Included", ui.ToneWarning
		action = "Exclude adult results"
	}

	return ui.Section("Adult content",
		statusRow(
			ui.Badge(label, tone),
			ui.Button(action, "secondary", ui.OnTap(ui.Invoke("configureModule", configureInput(toggled))))))
}

// catalogSection lists the user's own `/discover` catalogs and adds more.
//
// Two fields rather than a filter builder, and that is a deliberate trade: the
// alternative models every discover parameter TMDB has and goes stale the moment
// it adds one. A raw query is a power-user surface — it says so — and it means
// the whole of `/discover` is reachable rather than the subset somebody found
// time to build a control for.
func catalogSection(s settings) *ui.Element {
	els := []ui.El{
		ui.Banner("Build your own catalogs from TMDB's discover API. The query is raw discover parameters — see themoviedb.org's API docs for the full list.", ui.ToneInfo),
	}

	for i, c := range s.Catalogs {
		kind := "Films"
		if c.Type == typeTV {
			kind = "Series"
		}
		removed := s
		removed.Catalogs = withoutCatalog(s.Catalogs, i)
		els = append(els, statusRow(
			ui.Component("Text", ui.Prop("text", c.Name),
				ui.Prop("style", map[string]any{"weight": "medium"})),
			ui.Badge(kind, ui.ToneNeutral),
			ui.Component("Text", ui.Prop("text", c.Query),
				ui.Prop("style", map[string]any{"variant": "sm", "color": "text-muted", "lineClamp": 1})),
			ui.Button("Remove", "danger", ui.OnTap(ui.Invoke("configureModule", configureInput(removed))))))
	}

	if len(s.Catalogs) == 0 {
		els = append(els, ui.EmptyState("collections", "No custom catalogs yet"))
	}

	els = append(els,
		addCatalogField(s, typeMovie, "Add a film catalog", "French Thrillers | with_genres=53&with_original_language=fr"),
		addCatalogField(s, typeTV, "Add a series catalog", "Recent Sci-Fi | with_genres=10765&first_air_date.gte=2020-01-01"))

	return ui.Section("Custom catalogs", els...)
}

// addCatalogField is one "name | query" submit field. The pair is encoded in a
// single field because a SubmitField submits on its own — two fields would need
// somewhere to hold the half-finished value between them, and a module's
// settings screen has no such state.
func addCatalogField(s settings, nativeType, label, placeholder string) *ui.Element {
	// The typed value lands whole in Name, and settingsFrom splits it on the
	// first "|" when it reads the document back. One placeholder rather than two:
	// the runtime substitutes "$value" *everywhere* in the action, so a Name and
	// a Query placeholder would both receive the entire string.
	pending := s
	pending.Catalogs = append(append([]customCatalog{}, s.Catalogs...),
		customCatalog{Name: "$value", Type: nativeType})

	return ui.Component("Box",
		ui.Prop("style", map[string]any{"direction": "column", "gap": 2}),
		ui.Group(
			ui.Component("Text", ui.Prop("text", label),
				ui.Prop("style", map[string]any{"weight": "medium"})),
			ui.Component("SubmitField",
				ui.Prop("placeholder", placeholder),
				ui.Prop("submitLabel", "Add"),
				ui.OnTap(ui.Invoke("configureModule", configureInput(pending))))))
}

// withoutCatalog returns the list with the entry at index removed.
func withoutCatalog(catalogs []customCatalog, index int) []customCatalog {
	out := make([]customCatalog, 0, len(catalogs))
	for i, c := range catalogs {
		if i != index {
			out = append(out, c)
		}
	}
	return out
}

// attributionSection carries TMDB's required attribution. It is rendered rather
// than buried in a README because the terms attach the requirement to the
// product that uses the API, and a self-hosted deployment's product surface is
// this screen.
func attributionSection() *ui.Element {
	return ui.Section("About",
		ui.Banner("This product uses the TMDB API but is not endorsed or certified by TMDB.", ui.ToneInfo),
		ui.Button("Open themoviedb.org", "ghost", ui.OnTap(ui.OpenURL("https://www.themoviedb.org"))))
}

// statusRow lays controls out in a wrapping row, which is what a badge plus a
// button wants on a narrow screen.
func statusRow(els ...ui.El) *ui.Element {
	return ui.Component("Box",
		ui.Prop("style", map[string]any{"direction": "row", "align": "center", "gap": 2, "wrap": true}),
		ui.Group(els...))
}

// labelledRow stacks a field under its name and current value, so a form reads
// as settings rather than as a row of anonymous inputs.
func labelledRow(name, value string, field ui.El) *ui.Element {
	header := statusRow(
		ui.Component("Text", ui.Prop("text", name),
			ui.Prop("style", map[string]any{"weight": "medium"})),
		ui.Badge(value, ui.ToneNeutral))
	return ui.Component("Box",
		ui.Prop("style", map[string]any{"direction": "column", "gap": 2}),
		ui.Group(header, field))
}

// maskKey renders a credential as evidence that one is set without reproducing
// it. A settings screen is a page a user may well screenshot when asking for
// help, and the key is a secret; the last four characters are enough to tell two
// keys apart and useless to anyone else.
func maskKey(key string) string {
	const shown = 4
	if len(key) <= shown {
		return "••••"
	}
	return "••••" + key[len(key)-shown:]
}

// keyKindLabel names which of TMDB's two credentials is in use. A user who
// pasted the wrong one from their account page gets an answer here rather than
// from a 401 with no explanation.
func keyKindLabel(key string) string {
	if isBearerToken(key) {
		return "v4 read access token"
	}
	return "v3 API key"
}

// regionLabel renders an unset region as the behaviour rather than as a blank.
func regionLabel(region string) string {
	if region == "" {
		return "Not set"
	}
	return region
}
