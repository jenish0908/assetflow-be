package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Fake is a deterministic, rule-based stand-in for a chat model, used in
// tests and when no OPENAI_API_KEY is set. It turns simple English commands
// into tool calls, then summarises the tool results.
//
// It deliberately ignores which tools it was offered and calls whatever the
// text asks for, like a confused or prompt-injected model would; the agent's
// own permission check is what stops it.
//
// Clauses can be chained with ";" or "then":
//
//	allocate laptop L-102 to Priya
//	create laptop L-200 named ThinkPad X1; show L-200
//	show L-1, L-2 and L-3
type Fake struct{}

const tagPattern = `([A-Za-z0-9-]*\d[A-Za-z0-9-]*)` // a tag contains at least one digit

var (
	clauseSplit = regexp.MustCompile(`(?i)\s*(?:;|\bthen\b)\s*`)
	tagRe       = regexp.MustCompile(tagPattern)

	fakeRules = []struct {
		re    *regexp.Regexp
		build func(m []string) ToolCall
	}{
		{
			regexp.MustCompile(`(?i)^(?:please\s+)?allocate\s+(?:(?:the|a|an)\s+)?(?:[a-z]+\s+)?` + tagPattern + `\s+to\s+(.+)$`),
			func(m []string) ToolCall {
				return call("allocateAsset", map[string]string{"asset_tag": m[1], "user": m[2]})
			},
		},
		{
			regexp.MustCompile(`(?i)^create\s+(?:an?\s+)?(?:new\s+)?([a-z]+)\s+` + tagPattern + `\s+(?:named|called)\s+(.+)$`),
			func(m []string) ToolCall {
				return call("createAsset", map[string]string{"category": strings.ToLower(m[1]), "tag": m[2], "name": m[3]})
			},
		},
		{
			regexp.MustCompile(`(?i)^add\s+(?:user\s+)?(.+?)\s+<?([^\s<>]+@[^\s<>]+?)>?\s+as\s+(?:an?\s+)?(admin|manager|viewer)$`),
			func(m []string) ToolCall {
				return call("addUser", map[string]string{"name": m[1], "email": m[2], "role": strings.ToLower(m[3])})
			},
		},
		{
			regexp.MustCompile(`(?i)^(?:mark|set|move)\s+` + tagPattern + `\s+(?:as\s+|to\s+)?(available|maintenance|retired)$`),
			func(m []string) ToolCall {
				return call("updateAssetStatus", map[string]string{"asset_tag": m[1], "status": strings.ToLower(m[2])})
			},
		},
		{
			regexp.MustCompile(`(?i)^retire\s+` + tagPattern + `$`),
			func(m []string) ToolCall {
				return call("updateAssetStatus", map[string]string{"asset_tag": m[1], "status": "retired"})
			},
		},
		{
			regexp.MustCompile(`(?i)^rename\s+` + tagPattern + `\s+to\s+(.+)$`),
			func(m []string) ToolCall {
				return call("updateAsset", map[string]string{"asset_tag": m[1], "name": m[2]})
			},
		},
	}
	showRe = regexp.MustCompile(`(?i)^(?:show|get|check|info(?:rmation)?\s+(?:on|about)|tell me about|who has|what(?:'s| is)(?: the)? status of)\s+(.+)$`)
)

func call(name string, args map[string]string) ToolCall {
	b, _ := json.Marshal(args)
	return ToolCall{Name: name, Arguments: string(b)}
}

func (Fake) Chat(_ context.Context, req ChatRequest) (Message, error) {
	if len(req.Messages) == 0 {
		return Message{}, fmt.Errorf("fake llm: no messages")
	}
	if last := req.Messages[len(req.Messages)-1]; last.Role == RoleTool {
		return Message{Role: RoleAssistant, Content: summarize(req.Messages)}, nil
	}

	var calls []ToolCall
	for _, clause := range clauseSplit.Split(lastUserText(req.Messages), -1) {
		calls = append(calls, parseClause(strings.TrimRight(strings.TrimSpace(clause), ".!?"))...)
	}
	if len(calls) == 0 {
		return Message{Role: RoleAssistant, Content: "I can look up, create, rename, allocate and change the status of assets, " +
			`and add users. Try "allocate laptop L-102 to Priya" or "show L-102".`}, nil
	}
	for i := range calls {
		calls[i].ID = fmt.Sprintf("call_%d", i+1)
	}
	return Message{Role: RoleAssistant, ToolCalls: calls}, nil
}

func parseClause(clause string) []ToolCall {
	for _, r := range fakeRules {
		if m := r.re.FindStringSubmatch(clause); m != nil {
			return []ToolCall{r.build(m)}
		}
	}
	if m := showRe.FindStringSubmatch(clause); m != nil {
		var calls []ToolCall
		for _, tag := range tagRe.FindAllString(m[1], -1) {
			calls = append(calls, call("getAssetInfo", map[string]string{"asset_tag": tag}))
		}
		return calls
	}
	return nil
}

func lastUserText(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == RoleUser {
			return msgs[i].Content
		}
	}
	return ""
}

// summarize describes the results of the most recent round of tool calls.
func summarize(msgs []Message) string {
	results := map[string]string{}
	var turn *Message
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == RoleTool {
			results[msgs[i].ToolCallID] = msgs[i].Content
			continue
		}
		turn = &msgs[i]
		break
	}
	if turn == nil {
		return "Done."
	}

	var lines []string
	for _, c := range turn.ToolCalls {
		var res struct {
			OK     bool            `json:"ok"`
			Error  string          `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		_ = json.Unmarshal([]byte(results[c.ID]), &res)
		var args map[string]string
		_ = json.Unmarshal([]byte(c.Arguments), &args)

		if !res.OK {
			lines = append(lines, fmt.Sprintf("Could not %s: %s.", describe(c.Name, args), res.Error))
			continue
		}
		lines = append(lines, success(c.Name, args, res.Result))
	}
	return strings.Join(lines, " ")
}

func describe(name string, args map[string]string) string {
	switch name {
	case "allocateAsset":
		return fmt.Sprintf("allocate %s to %s", args["asset_tag"], args["user"])
	case "createAsset":
		return "create " + args["tag"]
	case "addUser":
		return "add " + args["email"]
	case "updateAssetStatus":
		return fmt.Sprintf("mark %s as %s", args["asset_tag"], args["status"])
	case "updateAsset":
		return "update " + args["asset_tag"]
	case "getAssetInfo":
		return "find " + args["asset_tag"]
	}
	return "run " + name
}

func success(name string, args map[string]string, result json.RawMessage) string {
	switch name {
	case "allocateAsset":
		var r struct{ User, Email string }
		_ = json.Unmarshal(result, &r)
		return fmt.Sprintf("Allocated %s to %s (%s).", args["asset_tag"], r.User, r.Email)
	case "createAsset":
		return fmt.Sprintf("Created %s %s.", args["category"], args["tag"])
	case "addUser":
		return fmt.Sprintf("Added %s as %s; they get a generated password.", args["email"], args["role"])
	case "updateAssetStatus":
		return fmt.Sprintf("%s is now %s.", args["asset_tag"], args["status"])
	case "updateAsset":
		return fmt.Sprintf("Updated %s.", args["asset_tag"])
	case "getAssetInfo":
		var r struct {
			Tag, Name, Status string
			Allocations       []struct {
				UserID     int64           `json:"user_id"`
				ReturnedAt json.RawMessage `json:"returned_at"`
			}
		}
		_ = json.Unmarshal(result, &r)
		s := fmt.Sprintf("%s (%s) is %s", r.Tag, r.Name, r.Status)
		if len(r.Allocations) > 0 && r.Allocations[0].ReturnedAt == nil {
			s += fmt.Sprintf(", held by user #%d", r.Allocations[0].UserID)
		}
		return s + "."
	}
	return "Done."
}
