package relay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/streams"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var opencodeAliasPattern = regexp.MustCompile(`(?i)deepseek HARNESS|opencode|grok build|liveagent|novaveilagent|workbuddy|zcode`)

const (
	opencodeAliasPrefix           = "opencode_"
	opencodeAliasCollisionTries   = 128
	opencodeAliasMaxChannels      = 256
	opencodeAliasMaxTemplateBytes = 16 * 1024
)

type opencodeAliasState struct {
	aliases       map[string]string
	replacer      *strings.Replacer
	prefixes      map[string]struct{}
	maxAliasBytes int
}

func applyOpencodeAlias(body []byte, format llm.APIFormat) ([]byte, *opencodeAliasState, error) {
	if !opencodeAliasFormat(format) {
		return body, nil, nil
	}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, nil, errors.New("opencode identity request must be a JSON object")
	}
	paths := opencodePromptPaths(body, format)
	originals := make(map[string]string)
	state := &opencodeAliasState{aliases: make(map[string]string), prefixes: make(map[string]struct{})}
	var edits []opencodeAliasEdit
	for _, path := range paths {
		value := gjson.GetBytes(body, path)
		text := value.String()
		matches := opencodeAliasPattern.FindAllStringIndex(text, -1)
		var out strings.Builder
		start := 0
		for _, match := range matches {
			if !opencodeNameBoundary(text, match[0], match[1]) {
				continue
			}
			original := text[match[0]:match[1]]
			alias, exists := originals[original]
			if !exists {
				var err error
				alias, err = opencodeIdentityAlias(body, original, state.aliases)
				if err != nil {
					return nil, nil, err
				}
				originals[original] = alias
				state.aliases[alias] = original
			}
			out.WriteString(text[start:match[0]])
			out.WriteString(alias)
			start = match[1]
		}
		if start != 0 {
			out.WriteString(text[start:])
			edits = append(edits, opencodeAliasEdit{path: path, text: out.String()})
		}
	}
	if len(edits) == 0 {
		return body, nil, nil
	}
	out, err := opencodeApplyEdits(body, edits)
	if err != nil {
		return nil, nil, err
	}
	aliases := make([]string, 0, len(state.aliases))
	for alias := range state.aliases {
		aliases = append(aliases, alias)
		state.maxAliasBytes = max(state.maxAliasBytes, len(alias))
		for n := 1; n < len(alias); n++ {
			state.prefixes[alias[:n]] = struct{}{}
		}
	}
	sort.Strings(aliases)
	pairs := make([]string, 0, 2*len(state.aliases))
	for _, alias := range aliases {
		pairs = append(pairs, alias, state.aliases[alias])
	}
	state.replacer = strings.NewReplacer(pairs...)
	return out, state, nil
}

func opencodeAliasFormat(format llm.APIFormat) bool {
	return format == llm.APIFormatOpenAIChatCompletion || format == llm.APIFormatAnthropicMessage || format == llm.APIFormatOpenAIResponse
}

func opencodeNameBoundary(text string, start, end int) bool {
	word := func(b byte) bool {
		return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
	}
	return (start == 0 || !word(text[start-1])) && (end == len(text) || !word(text[end]))
}

func opencodeIdentityAlias(body []byte, original string, aliases map[string]string) (string, error) {
	for probe := 0; probe < opencodeAliasCollisionTries; probe++ {
		sum := sha256.Sum256([]byte("opencode-identity\x00" + original + "\x00" + itoa(probe)))
		alias := opencodeAliasPrefix + hex.EncodeToString(sum[:16])
		if _, exists := aliases[alias]; exists {
			continue
		}
		if bytes.Contains(body, []byte(alias)) || opencodeJSONContains(gjson.ParseBytes(body), alias) {
			continue
		}
		return alias, nil
	}
	return "", errors.New("opencode identity alias collision limit exceeded")
}

func opencodeJSONContains(value gjson.Result, alias string) bool {
	if value.Type == gjson.String {
		return strings.Contains(value.String(), alias)
	}
	if !value.IsArray() && !value.IsObject() {
		return false
	}
	found := false
	value.ForEach(func(key, child gjson.Result) bool {
		found = strings.Contains(key.String(), alias) || opencodeJSONContains(child, alias)
		return !found
	})
	return found
}

func opencodePromptPaths(body []byte, format llm.APIFormat) []string {
	var paths []string
	switch format {
	case llm.APIFormatOpenAIChatCompletion:
		for i, message := range gjson.GetBytes(body, "messages").Array() {
			if role := message.Get("role").String(); role == "system" || role == "developer" {
				paths = append(paths, opencodeTextPaths(body, "messages."+itoa(i)+".content", "text", "input_text")...)
			}
		}
	case llm.APIFormatAnthropicMessage:
		paths = append(paths, opencodeTextPaths(body, "system", "text")...)
	case llm.APIFormatOpenAIResponse:
		paths = append(paths, opencodeTextPaths(body, "instructions", "text", "input_text")...)
		for i, message := range gjson.GetBytes(body, "input").Array() {
			if kind := message.Get("type").String(); kind != "" && kind != "message" {
				continue
			}
			if role := message.Get("role").String(); role == "system" || role == "developer" {
				paths = append(paths, opencodeTextPaths(body, "input."+itoa(i)+".content", "text", "input_text")...)
			}
		}
	}
	return paths
}

func opencodeTextPaths(body []byte, path string, kinds ...string) []string {
	value := gjson.GetBytes(body, path)
	if value.Type == gjson.String {
		return []string{path}
	}
	var paths []string
	for i, block := range value.Array() {
		for _, kind := range kinds {
			if block.Get("type").String() == kind && block.Get("text").Type == gjson.String {
				paths = append(paths, path+"."+itoa(i)+".text")
				break
			}
		}
	}
	return paths
}

type opencodeAliasEdit struct {
	path string
	text string
}

func opencodeApplyEdits(body []byte, edits []opencodeAliasEdit) ([]byte, error) {
	for _, edit := range edits {
		if gjson.GetBytes(body, edit.path).String() == edit.text {
			continue
		}
		var err error
		body, err = sjson.SetBytes(body, edit.path, edit.text)
		if err != nil {
			return nil, fmt.Errorf("opencode identity field %s: %w", edit.path, err)
		}
	}
	return body, nil
}

func restoreOpencodeAliasNonStream(body []byte, format llm.APIFormat, state *opencodeAliasState) ([]byte, error) {
	if state == nil || !opencodeAliasFormat(format) {
		return body, nil
	}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, errors.New("opencode identity response must be a JSON object")
	}
	return opencodeRestorePaths(body, opencodeResponsePaths(body, format, ""), state)
}

func opencodeRestorePaths(body []byte, paths []string, state *opencodeAliasState) ([]byte, error) {
	var edits []opencodeAliasEdit
	for _, path := range paths {
		value := gjson.GetBytes(body, path)
		if value.Type == gjson.String {
			edits = append(edits, opencodeAliasEdit{path: path, text: state.replacer.Replace(value.String())})
		}
	}
	return opencodeApplyEdits(body, edits)
}

func opencodeResponsePaths(body []byte, format llm.APIFormat, prefix string) []string {
	var paths []string
	switch format {
	case llm.APIFormatOpenAIChatCompletion:
		for i := range gjson.GetBytes(body, prefix+"choices").Array() {
			base := prefix + "choices." + itoa(i) + ".message"
			paths = append(paths, opencodeTextPaths(body, base+".content", "text", "output_text")...)
			paths = append(paths, base+".reasoning_content", base+".reasoning")
		}
	case llm.APIFormatAnthropicMessage:
		for i, block := range gjson.GetBytes(body, prefix+"content").Array() {
			base := prefix + "content." + itoa(i)
			switch block.Get("type").String() {
			case "text":
				paths = append(paths, base+".text")
			case "thinking":
				paths = append(paths, base+".thinking")
			}
		}
	case llm.APIFormatOpenAIResponse:
		paths = append(paths, prefix+"output_text")
		for i := range gjson.GetBytes(body, prefix+"output").Array() {
			paths = append(paths, opencodeResponseItemPaths(body, prefix+"output."+itoa(i))...)
		}
	}
	return paths
}

func opencodeResponseItemPaths(body []byte, base string) []string {
	switch gjson.GetBytes(body, base+".type").String() {
	case "message":
		return opencodeTextPaths(body, base+".content", "output_text", "text")
	case "reasoning":
		paths := opencodeTextPaths(body, base+".summary", "summary_text")
		return append(paths, opencodeTextPaths(body, base+".content", "reasoning_text", "text")...)
	}
	return nil
}

func applyOpencodeIdentityRestore(resp *upstreamResponse, format llm.APIFormat, state *opencodeAliasState) error {
	if resp == nil || state == nil || !opencodeAliasFormat(format) {
		return nil
	}
	if resp.events == nil && len(resp.window) == 0 {
		body, err := restoreOpencodeAliasNonStream(resp.body, format, state)
		if err != nil {
			return err
		}
		resp.body = body
		return nil
	}
	restorer := &opencodeIdentityStream{
		inner: resp.events, format: format, state: state,
		pending: make(map[string]opencodeIdentityPending), ended: resp.last,
	}
	var window []*httpclient.StreamEvent
	for _, event := range resp.window {
		out, err := restorer.transform(event)
		if err != nil {
			return err
		}
		out, err = restorer.renumber(out)
		if err != nil {
			return err
		}
		window = append(window, out...)
	}
	if resp.last || resp.events == nil {
		out, err := restorer.flush(nil)
		if err != nil {
			return err
		}
		out, err = restorer.renumber(out)
		if err != nil {
			return err
		}
		window = append(window, out...)
		restorer.ended = true
	}
	resp.window = window
	resp.events = restorer
	return nil
}

type opencodeIdentityPending struct {
	text     string
	template *httpclient.StreamEvent
	path     string
}

type opencodeIdentityStream struct {
	inner           streams.Stream[*httpclient.StreamEvent]
	format          llm.APIFormat
	state           *opencodeAliasState
	pending         map[string]opencodeIdentityPending
	queue           []*httpclient.StreamEvent
	current         *httpclient.StreamEvent
	ended           bool
	err             error
	sequenceChecked bool
	sequenceEnabled bool
	nextSequence    uint64
	closeOnce       sync.Once
	closeMu         sync.Mutex
	closeErr        error
}

func (s *opencodeIdentityStream) Next() bool {
	if len(s.queue) == 0 && !s.ended {
		if s.inner != nil && s.inner.Next() {
			var err error
			s.queue, err = s.transform(s.inner.Current())
			if err != nil {
				s.err = errors.Join(s.err, err)
				s.ended = true
			}
		} else {
			s.ended = true
		}
		if s.ended {
			out, err := s.flush(nil)
			s.err = errors.Join(s.err, err)
			s.queue = append(s.queue, out...)
		}
		var err error
		s.queue, err = s.renumber(s.queue)
		if err != nil {
			s.err = errors.Join(s.err, err)
			s.ended = true
		}
	}
	if len(s.queue) == 0 {
		s.current = nil
		return false
	}
	s.current, s.queue = s.queue[0], s.queue[1:]
	return true
}

func (s *opencodeIdentityStream) Current() *httpclient.StreamEvent { return s.current }

func (s *opencodeIdentityStream) Err() error {
	s.closeMu.Lock()
	closeErr := s.closeErr
	s.closeMu.Unlock()
	var innerErr error
	if s.inner != nil {
		innerErr = s.inner.Err()
	}
	return errors.Join(s.err, innerErr, closeErr)
}

func (s *opencodeIdentityStream) Close() error {
	s.closeOnce.Do(func() {
		var err error
		if s.inner != nil {
			err = s.inner.Close()
		}
		s.closeMu.Lock()
		s.closeErr = err
		s.closeMu.Unlock()
	})
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closeErr
}

type opencodeIdentityField struct {
	key          string
	path         string
	templatePath string
	kind         string
	index        int
	block        int
}

func (s *opencodeIdentityStream) transform(event *httpclient.StreamEvent) ([]*httpclient.StreamEvent, error) {
	if event == nil || len(event.Data) == 0 {
		return []*httpclient.StreamEvent{event}, nil
	}
	if !bytes.Equal(bytes.TrimSpace(event.Data), []byte("[DONE]")) && (!gjson.ValidBytes(event.Data) || !gjson.ParseBytes(event.Data).IsObject()) {
		return nil, errors.New("opencode identity stream event must be a JSON object")
	}
	if s.format == llm.APIFormatOpenAIResponse && !s.sequenceChecked {
		s.sequenceChecked = true
		if value := gjson.GetBytes(event.Data, "sequence_number"); value.Exists() {
			var number int64
			if err := json.Unmarshal([]byte(value.Raw), &number); err != nil {
				return nil, fmt.Errorf("opencode identity response sequence_number: %w", err)
			}
			if value.Type != gjson.Number || number < 0 {
				return nil, errors.New("opencode identity invalid response sequence_number")
			}
			s.sequenceEnabled = true
			s.nextSequence = uint64(number)
		}
	}
	verdict := analyzeStreamEvent(s.format, event)
	if s.err == nil {
		s.err = errors.Join(verdict.inspectErr, verdict.abnormalErr)
	}
	if verdict.terminal {
		s.ended = true
	}
	all := verdict.terminal || verdict.inspectErr != nil || verdict.abnormalErr != nil
	closing := s.closingChannels(event.Data)
	isClosing := func(key string) bool {
		if all {
			return true
		}
		for _, prefix := range closing {
			if strings.HasPrefix(key, prefix) {
				return true
			}
		}
		return false
	}
	data := event.Data
	for _, field := range s.fields(data) {
		value := gjson.GetBytes(data, field.path)
		if value.Type != gjson.String {
			continue
		}
		text := s.pending[field.key].text + value.String()
		keep := 0
		if !isClosing(field.key) {
			for n := min(len(text), s.state.maxAliasBytes-1); n > 0; n-- {
				if _, exists := s.state.prefixes[text[len(text)-n:]]; exists {
					keep = n
					break
				}
			}
		}
		output := s.state.replacer.Replace(text[:len(text)-keep])
		updated, err := opencodeApplyEdits(data, []opencodeAliasEdit{{path: field.path, text: output}})
		if err != nil {
			return nil, err
		}
		if keep == 0 {
			delete(s.pending, field.key)
		} else {
			if _, exists := s.pending[field.key]; !exists && len(s.pending) >= opencodeAliasMaxChannels {
				return nil, errors.New("opencode identity pending channel limit exceeded")
			}
			template, err := s.template(event, field)
			if err != nil {
				return nil, err
			}
			s.pending[field.key] = opencodeIdentityPending{text: strings.Clone(text[len(text)-keep:]), template: template, path: field.templatePath}
		}
		data = updated
	}
	var err error
	data, err = opencodeRestorePaths(data, s.snapshotPaths(data), s.state)
	if err != nil {
		return nil, err
	}
	out, err := s.flush(isClosing)
	if err != nil {
		return nil, err
	}
	copyEvent := *event
	copyEvent.Data = data
	return append(out, &copyEvent), nil
}

func (s *opencodeIdentityStream) flush(matches func(string) bool) ([]*httpclient.StreamEvent, error) {
	keys := make([]string, 0, len(s.pending))
	for key := range s.pending {
		if matches == nil || matches(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := make([]*httpclient.StreamEvent, 0, len(keys))
	for _, key := range keys {
		pending := s.pending[key]
		data, err := sjson.SetBytes(pending.template.Data, pending.path, pending.text)
		if err != nil {
			return nil, fmt.Errorf("opencode identity flush: %w", err)
		}
		event := *pending.template
		event.Data = data
		out = append(out, &event)
		delete(s.pending, key)
	}
	return out, nil
}

func (s *opencodeIdentityStream) renumber(events []*httpclient.StreamEvent) ([]*httpclient.StreamEvent, error) {
	if !s.sequenceEnabled {
		return events, nil
	}
	for i, event := range events {
		if event == nil || len(event.Data) == 0 {
			continue
		}
		if s.nextSequence > math.MaxInt64 {
			return nil, errors.New("opencode identity response sequence_number overflow")
		}
		data, err := sjson.SetBytes(event.Data, "sequence_number", s.nextSequence)
		if err != nil {
			return nil, fmt.Errorf("opencode identity response sequence_number: %w", err)
		}
		copyEvent := *event
		copyEvent.Data = data
		events[i] = &copyEvent
		s.nextSequence++
	}
	return events, nil
}

func (s *opencodeIdentityStream) fields(data []byte) []opencodeIdentityField {
	var fields []opencodeIdentityField
	switch s.format {
	case llm.APIFormatOpenAIChatCompletion:
		for i, choice := range gjson.GetBytes(data, "choices").Array() {
			index := opencodeIdentityIndex(choice.Get("index"), i)
			base := "choices." + itoa(i) + ".delta."
			key := "chat:" + itoa(index) + ":"
			content := choice.Get("delta.content")
			if content.Type == gjson.String {
				fields = append(fields, opencodeIdentityField{key: key + "content", path: base + "content", templatePath: "choices.0.delta.content", kind: "content", index: index})
			}
			for j, block := range content.Array() {
				if kind := block.Get("type").String(); kind != "text" && kind != "output_text" {
					continue
				}
				blockIndex := opencodeIdentityIndex(block.Get("index"), j)
				fields = append(fields, opencodeIdentityField{key: key + "content:" + itoa(blockIndex), path: base + "content." + itoa(j) + ".text", templatePath: "choices.0.delta.content.0.text", kind: block.Get("type").String(), index: index, block: blockIndex})
			}
			for _, field := range []string{"reasoning_content", "reasoning"} {
				fields = append(fields, opencodeIdentityField{key: key + field, path: base + field, templatePath: "choices.0.delta." + field, kind: field, index: index})
			}
		}
	case llm.APIFormatAnthropicMessage:
		index := int(gjson.GetBytes(data, "index").Int())
		base := "delta"
		kind := gjson.GetBytes(data, "delta.type").String()
		switch gjson.GetBytes(data, "type").String() {
		case "content_block_start":
			base = "content_block"
			kind = gjson.GetBytes(data, "content_block.type").String()
		case "content_block_delta":
		default:
			return nil
		}
		field := ""
		switch kind {
		case "text", "text_delta":
			field = "text"
		case "thinking", "thinking_delta":
			field = "thinking"
		}
		if field != "" {
			fields = append(fields, opencodeIdentityField{key: "anthropic:" + itoa(index) + ":" + field, path: base + "." + field, templatePath: "delta." + field, kind: field, index: index})
		}
	case llm.APIFormatOpenAIResponse:
		kind := gjson.GetBytes(data, "type").String()
		if kind == "response.output_text.delta" || kind == "response.reasoning_text.delta" || kind == "response.reasoning_summary_text.delta" {
			fields = append(fields, opencodeIdentityField{key: opencodeResponsesChannel(data, kind), path: "delta", templatePath: "delta", kind: kind})
		}
	}
	return fields
}

func opencodeIdentityIndex(value gjson.Result, fallback int) int {
	if value.Type == gjson.Number {
		return int(value.Int())
	}
	return fallback
}

func opencodeResponsesChannel(data []byte, kind string) string {
	part := gjson.GetBytes(data, "content_index").Int()
	if strings.Contains(kind, "reasoning_summary") {
		part = gjson.GetBytes(data, "summary_index").Int()
	}
	return "responses:" + itoa(int(gjson.GetBytes(data, "output_index").Int())) + ":" + strings.TrimSuffix(strings.TrimSuffix(kind, ".delta"), ".done") + ":" + itoa(int(part)) + ":"
}

func (s *opencodeIdentityStream) closingChannels(data []byte) []string {
	var prefixes []string
	switch s.format {
	case llm.APIFormatOpenAIChatCompletion:
		for i, choice := range gjson.GetBytes(data, "choices").Array() {
			if finish := choice.Get("finish_reason"); finish.Type == gjson.String && finish.String() != "" {
				prefixes = append(prefixes, "chat:"+itoa(opencodeIdentityIndex(choice.Get("index"), i))+":")
			}
		}
	case llm.APIFormatAnthropicMessage:
		switch gjson.GetBytes(data, "type").String() {
		case "content_block_stop":
			prefixes = append(prefixes, "anthropic:"+itoa(int(gjson.GetBytes(data, "index").Int()))+":")
		case "message_delta":
			if gjson.GetBytes(data, "delta.stop_reason").Type == gjson.String {
				prefixes = append(prefixes, "anthropic:")
			}
		}
	case llm.APIFormatOpenAIResponse:
		kind := gjson.GetBytes(data, "type").String()
		switch kind {
		case "response.output_text.done", "response.reasoning_text.done", "response.reasoning_summary_text.done":
			prefixes = append(prefixes, opencodeResponsesChannel(data, kind))
		case "response.content_part.done", "response.reasoning_summary_part.done":
			switch gjson.GetBytes(data, "part.type").String() {
			case "output_text":
				prefixes = append(prefixes, opencodeResponsesChannel(data, "response.output_text.delta"))
			case "reasoning_text":
				prefixes = append(prefixes, opencodeResponsesChannel(data, "response.reasoning_text.delta"))
			case "summary_text":
				prefixes = append(prefixes, opencodeResponsesChannel(data, "response.reasoning_summary_text.delta"))
			}
		case "response.output_item.done":
			prefixes = append(prefixes, "responses:"+itoa(int(gjson.GetBytes(data, "output_index").Int()))+":")
		}
	}
	return prefixes
}

func (s *opencodeIdentityStream) snapshotPaths(data []byte) []string {
	switch s.format {
	case llm.APIFormatAnthropicMessage:
		if gjson.GetBytes(data, "type").String() == "message_start" {
			return opencodeResponsePaths(data, s.format, "message.")
		}
	case llm.APIFormatOpenAIResponse:
		switch gjson.GetBytes(data, "type").String() {
		case "response.output_text.done", "response.reasoning_text.done", "response.reasoning_summary_text.done":
			return []string{"text"}
		case "response.output_item.added", "response.output_item.done":
			return opencodeResponseItemPaths(data, "item")
		case "response.content_part.added", "response.content_part.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
			switch gjson.GetBytes(data, "part.type").String() {
			case "output_text", "reasoning_text", "summary_text":
				return []string{"part.text"}
			}
		case "response.created", "response.in_progress", "response.completed", "response.failed", "response.incomplete", "response.cancelled":
			return opencodeResponsePaths(data, s.format, "response.")
		}
	}
	return nil
}

func (s *opencodeIdentityStream) template(event *httpclient.StreamEvent, field opencodeIdentityField) (*httpclient.StreamEvent, error) {
	root := make(map[string]any)
	copyFields := func(fields ...string) {
		for _, name := range fields {
			if value := gjson.GetBytes(event.Data, name); value.Exists() {
				root[name] = json.RawMessage(value.Raw)
			}
		}
	}
	typeName := event.Type
	switch s.format {
	case llm.APIFormatOpenAIChatCompletion:
		copyFields("id", "object", "created", "model", "system_fingerprint", "service_tier")
		delta := make(map[string]any)
		if field.kind == "text" || field.kind == "output_text" {
			delta["content"] = []any{map[string]any{"type": field.kind, "index": field.block, "text": ""}}
		}
		root["choices"] = []any{map[string]any{"index": field.index, "delta": delta}}
	case llm.APIFormatAnthropicMessage:
		typeName = "content_block_delta"
		root["type"] = typeName
		root["index"] = field.index
		root["delta"] = map[string]any{"type": field.kind + "_delta"}
	case llm.APIFormatOpenAIResponse:
		typeName = field.kind
		copyFields("item_id", "output_index", "content_index", "summary_index")
		root["type"] = typeName
	}
	data, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("opencode identity delta template: %w", err)
	}
	if len(data)+len(typeName)+len(event.LastEventID) > opencodeAliasMaxTemplateBytes {
		return nil, errors.New("opencode identity delta template limit exceeded")
	}
	return &httpclient.StreamEvent{Type: strings.Clone(typeName), Data: data, LastEventID: strings.Clone(event.LastEventID)}, nil
}
