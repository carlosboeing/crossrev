package harness

import (
	"fmt"
	"strings"
)

// promptText is the text delivered by the adapter, including its instructions.
// Budgeting and delivery share this composition so wrappers cannot bypass a limit.
func promptText(name string, inv Invocation) string {
	switch name {
	case "opencode":
		text := inv.Prompt.Text
		if inv.Schema.Present() {
			text += fmt.Sprintf(schemaInstruction, inv.Schema.Argument())
		}
		return strings.TrimRight(text, "\n")
	case "grok":
		if inv.Schema.Present() && (inv.Write || inv.ReadMode == ReadModeSupplied) {
			return inv.Prompt.Text + fmt.Sprintf(schemaInstruction, inv.Schema.Argument())
		}
		return inv.Prompt.Text
	case "agy":
		// A denied command ends an Antigravity headless run with an empty
		// answer. Keep the directive that prevented this at agy 1.2.12.
		if !inv.Write {
			return reviewNoCommandsPrefix + inv.Prompt.Argument() + "\n\n" + reviewNoCommandsSuffix
		}
	}
	return inv.Prompt.Argument()
}

// InputBytes measures the prompt and output schema supplied to the harness.
// Native schemas count conservatively as context too, even when carried as a
// separate flag or file. Provider-added system prompts are not observable here.
func InputBytes(name string, inv Invocation) int {
	n := len(promptText(name, inv))
	embedded := name == "opencode" || (name == "grok" && (inv.Write || inv.ReadMode == ReadModeSupplied))
	if inv.Schema.Present() && !embedded {
		n += len(inv.Schema.Text)
	}
	return n
}
