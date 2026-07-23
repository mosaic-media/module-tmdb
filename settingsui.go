package tmdb

import (
	"context"

	v1 "github.com/mosaic-media/sdk/contracts/platform/v1"
	"github.com/mosaic-media/sdui/ui"
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
	return map[string]any{
		"moduleId": CapabilityID,
		"settings": map[string]any{
			"apiKey":       s.APIKey,
			"language":     s.Language,
			"region":       s.Region,
			"includeAdult": s.IncludeAdult,
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

	if s.APIKey == "" {
		return ui.Section("API key",
			ui.Banner("TMDB has no anonymous access, so metadata and search do nothing until a key is set. Create a free account at themoviedb.org, then copy the API key from Settings › API.", ui.ToneWarning),
			field)
	}

	cleared := s
	cleared.APIKey = ""
	return ui.Section("API key",
		ui.Banner("A key is configured. Saving a new one replaces it.", ui.ToneSuccess),
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
