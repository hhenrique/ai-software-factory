package harness

import "testing"

// These fixtures are trimmed from real `copilot --output-format json`
// output (v1.0.78) captured live — see copilotAdapter's doc comment for
// how that live signal was gathered (a real Reviewer-role call was
// silently returning Produced: null, TokensUsed: 0 because the previous
// parser looked for content/tokens at the event's top level; the real
// shape nests both under "data").

const singleTurnJSONL = `{"type":"session.mcp_server_status_changed","data":{"serverName":"github-mcp-server","status":"connected"},"ephemeral":true}
{"type":"user.message","data":{"content":"Reply with exactly this text and nothing else: hello world"}}
{"type":"assistant.message_start","data":{"messageId":"m1"},"ephemeral":true}
{"type":"assistant.message_delta","data":{"messageId":"m1","deltaContent":"hello"},"ephemeral":true}
{"type":"assistant.message_delta","data":{"messageId":"m1","deltaContent":" world"},"ephemeral":true}
{"type":"assistant.message","data":{"messageId":"m1","model":"gpt-5-mini","content":"hello world","toolRequests":[],"outputTokens":282}}
{"type":"assistant.turn_end","data":{"turnId":"0"}}
{"type":"result","exitCode":0,"usage":{"premiumRequests":0}}`

const multiTurnJSONL = `{"type":"assistant.message","data":{"messageId":"m1","content":"looking at the file now","toolRequests":[{"tool":"read_file"}],"outputTokens":40}}
{"type":"assistant.message","data":{"messageId":"m2","content":"` + "```json\\n{ \\\"verdict\\\": \\\"approved\\\" }\\n```" + `","toolRequests":[],"outputTokens":85}}`

func TestFinalTextFromJSONLUsesAssistantMessageDataContent(t *testing.T) {
	got := finalTextFromJSONL([]byte(singleTurnJSONL))
	want := "hello world"
	if got != want {
		t.Errorf("finalTextFromJSONL = %q, want %q", got, want)
	}
}

func TestFinalTextFromJSONLIgnoresStreamingDeltas(t *testing.T) {
	// The delta chunks ("hello", " world") must not leak into the result
	// on their own — only the complete assistant.message event's content.
	got := finalTextFromJSONL([]byte(singleTurnJSONL))
	if got == "hello" || got == " world" {
		t.Errorf("finalTextFromJSONL = %q, want the complete message, not a streaming delta", got)
	}
}

func TestFinalTextFromJSONLMultiTurnUsesLastAssistantMessage(t *testing.T) {
	got := finalTextFromJSONL([]byte(multiTurnJSONL))
	want := "```json\n{ \"verdict\": \"approved\" }\n```"
	if got != want {
		t.Errorf("finalTextFromJSONL = %q, want the last turn's content %q", got, want)
	}
}

func TestBestEffortTokenCountFromJSONLSumsOutputTokens(t *testing.T) {
	if got := bestEffortTokenCountFromJSONL([]byte(singleTurnJSONL)); got != 282 {
		t.Errorf("bestEffortTokenCountFromJSONL = %d, want 282", got)
	}
	if got := bestEffortTokenCountFromJSONL([]byte(multiTurnJSONL)); got != 125 {
		t.Errorf("bestEffortTokenCountFromJSONL = %d, want 125 (40+85, summed across both turns)", got)
	}
}
