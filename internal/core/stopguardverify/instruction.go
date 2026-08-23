package stopguardverify

import "strings"

// BuildInstruction returns the conservative verifier prompt implementing the
// design's six-rule contract, embedding the supplied bounded evidence block.
//
// The instruction is deliberately explicit about negative and positive rules
// so substring checks in tests remain stable.
func BuildInstruction(evidenceBlock string) string {
	var b strings.Builder
	b.Grow(2048 + len(evidenceBlock))

	b.WriteString("You are a semantic completion verifier for Agent Loop Guard.\n")
	b.WriteString("Decide whether the already requested user work is complete.\n\n")

	b.WriteString("Rules:\n")
	b.WriteString("1. Decide whether the already requested work is complete. Only the original user objective and the work already performed matter.\n")
	b.WriteString("2. Return CONTINUE only if you can name concrete unfinished work already requested by the user that is executable without new user input. You must provide a non-empty remaining_objective naming that concrete work.\n")
	b.WriteString("3. Do not count the following as unfinished work (they are NOT unfinished work): optional ideas, suggestions such as \"I can also...\" or \"I could also...\", user-owned \"Next steps\" lists assigned to the user, offers of help, future possibilities, or speculative follow-ups.\n")
	b.WriteString("4. If the next step requires any user answer, approval, permission, or choice, return NEEDS_USER. Do not assume or synthesize user responses.\n")
	b.WriteString("5. If evidence is insufficient, ambiguous, or you cannot decide safely, return UNCERTAIN.\n")
	b.WriteString("6. Output one structured verdict with a short bounded reason and optional remaining objective. Output MUST be a single JSON object with fields {\"kind\": \"...\", \"reason\": \"...\", \"remaining_objective\": \"...\"} where kind is one of \"allow_stop\", \"continue\", \"needs_user\", \"blocked\", \"uncertain\".\n")

	b.WriteString("\nNegative rules (do NOT authorize CONTINUE):\n")
	b.WriteString("- Completed answers that fully address the user objective are NOT unfinished work.\n")
	b.WriteString("- Optional \"I can also...\" suggestions are NOT unfinished work.\n")
	b.WriteString("- \"Next steps\" lists that are user-owned or require user action are NOT unfinished work.\n")
	b.WriteString("- Direct questions to the user (e.g., \"Would you like me to...?\", \"Should I...?\") are NOT unfinished work; they require NEEDS_USER.\n")
	b.WriteString("- Quoted future-action language (e.g., quoted \"I'll continue\" or \"I will do X\") alone without trajectory evidence is NOT unfinished work.\n")

	b.WriteString("\nPositive rule:\n")
	b.WriteString("- First-person immediate in-scope commitments not evidenced as executed (e.g., \"Let me run the tests next.\" or \"I will now...\" where the promised action has no matching tool/result in the trajectory) CAN authorize CONTINUE when the commitment is concrete, immediate, in scope, and lacks execution evidence.\n")

	b.WriteString("\nEvidence (bounded projection):\n")
	b.WriteString("<evidence>\n")
	if evidenceBlock != "" {
		// Ensure evidence is bounded inside instruction as well.
		if len(evidenceBlock) > MaxEvidenceBytes {
			evidenceBlock = truncateString(evidenceBlock, MaxEvidenceBytes)
		}
		b.WriteString(evidenceBlock)
		if !strings.HasSuffix(evidenceBlock, "\n") {
			b.WriteString("\n")
		}
	} else {
		b.WriteString("(none)\n")
	}
	b.WriteString("</evidence>\n")

	b.WriteString("\nOutput contract: single JSON object {\"kind\": \"...\", \"reason\": \"...\", \"remaining_objective\": \"...\"}. Keep reason and remaining_objective bounded (<=512 bytes each). Do NOT include chain-of-thought.\n")

	return b.String()
}
