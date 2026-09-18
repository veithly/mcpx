package source

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// SmartQueryOptions controls the exact/token search gateway.
type SmartQueryOptions struct {
	Query           string
	Mode            string
	Parallel        bool
	MaxResults      int
	Cursor          string
	Pattern         string
	ExcludePattern  string
	ContextBefore   int
	ContextAfter    int
	MaxBytesPerFile int64
	IncludeSHA256   bool
	ScopePaths      []string
	Allowed         func(string) bool
}

type smartDocument struct {
	Path    string
	Content string
	Title   string
	SHA256  string
}

type smartCandidate struct {
	Path           string
	Score          int
	Sources        map[string]bool
	Matches        map[string]bool
	BestLine       int
	HasBestLine    bool
	WindowScore    int
	MatchReason    string
	ReasonPriority int
}

// SmartQueryPage runs exact and token recall, then merges the ranked files.
// The analyzer is local and deterministic; a model-backed QueryAnalyzer can
// be introduced later without changing the search or response contracts.
func SmartQueryPage(root string, opts SmartQueryOptions) (map[string]any, error) {
	if strings.TrimSpace(opts.Query) == "" {
		return nil, fmt.Errorf("query required")
	}
	mode := strings.ToLower(strings.TrimSpace(opts.Mode))
	if mode == "" {
		mode = "smart"
	}
	if mode != "smart" && mode != "exact" && mode != "token" {
		return nil, fmt.Errorf("unsupported query mode %q", mode)
	}
	if opts.MaxResults <= 0 || opts.MaxResults > 20 {
		opts.MaxResults = 20
	}
	if opts.MaxBytesPerFile <= 0 {
		opts.MaxBytesPerFile = 64 << 10
	}
	analysis := AnalyzeQuery(opts.Query)
	paths, err := paths(root, opts.Pattern, opts.ExcludePattern, opts.Allowed, opts.ScopePaths...)
	if err != nil {
		return nil, err
	}
	documents := loadSmartDocuments(root, paths)

	runExact := mode == "smart" || mode == "exact"
	runToken := mode == "smart" || mode == "token"
	var exactCandidates, tokenCandidates []smartCandidate
	if opts.Parallel && runExact && runToken {
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			exactCandidates = exactRecall(opts.Query, documents)
		}()
		go func() {
			defer wait.Done()
			tokenCandidates = tokenRecall(analysis, documents)
		}()
		wait.Wait()
	} else {
		if runExact {
			exactCandidates = exactRecall(opts.Query, documents)
		}
		if runToken {
			tokenCandidates = tokenRecall(analysis, documents)
		}
	}

	merged := make(map[string]*smartCandidate, len(exactCandidates)+len(tokenCandidates))
	for _, candidate := range append(exactCandidates, tokenCandidates...) {
		mergeSmartCandidate(merged, candidate)
	}
	ordered := make([]*smartCandidate, 0, len(merged))
	for _, candidate := range merged {
		ordered = append(ordered, candidate)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Score != ordered[j].Score {
			return ordered[i].Score > ordered[j].Score
		}
		if ordered[i].Sources["exact"] != ordered[j].Sources["exact"] {
			return ordered[i].Sources["exact"]
		}
		return ordered[i].Path < ordered[j].Path
	})

	start := decodeCursor(opts.Cursor)
	if start > len(ordered) {
		start = len(ordered)
	}
	end := start + opts.MaxResults
	if end > len(ordered) {
		end = len(ordered)
	}
	files := make([]map[string]any, 0, end-start)
	totalBytes := 0
	for pageIndex, candidate := range ordered[start:end] {
		offset, limit := smartContextWindow(candidate, opts.ContextBefore, opts.ContextAfter)
		read, readErr := Read(root, candidate.Path, offset, limit, opts.MaxBytesPerFile)
		matchedTerms := smartMatches(candidate.Matches)
		entry := map[string]any{
			"path": candidate.Path, "score": candidate.Score, "rank": start + pageIndex + 1,
			"source": smartSources(candidate.Sources), "matches": matchedTerms, "matched_terms": matchedTerms,
			"match_reason": candidate.MatchReason, "metadata": smartMetadata(candidate.Path),
		}
		if readErr != nil {
			entry["ok"] = false
			entry["error"] = readErr.Error()
		} else {
			entry["ok"] = true
			entry["content"] = read.Content
			entry["offset"] = read.Offset
			entry["limit"] = read.Limit
			entry["total_lines"] = read.TotalLines
			entry["truncated"] = read.Truncated
			totalBytes += len(read.Content)
			if opts.IncludeSHA256 {
				entry["sha256"] = read.SHA256
			}
		}
		files = append(files, entry)
	}
	result := map[string]any{
		"query": opts.Query, "mode": mode, "parallel": opts.Parallel,
		"analysis": analysis, "files": files, "total_bytes": totalBytes,
		"truncated": end < len(ordered), "max_results": opts.MaxResults,
	}
	if end < len(ordered) {
		result["next_cursor"] = encodeCursor(end)
	}
	return result, nil
}

// ScopePathFilter turns user-provided paths into a hard file boundary. A
// directory seed includes only that directory's descendants; a file seed
// includes only that file. Invalid or missing seeds match nothing instead of
// silently widening the query back to the workspace root.
func ScopePathFilter(root string, seeds []string, allowed func(string) bool) func(string) bool {
	scopes := normalizedScopePaths(root, seeds)
	return func(path string) bool {
		if allowed != nil && !allowed(path) {
			return false
		}
		return pathIntersectsScopes(filepath.ToSlash(filepath.Clean(path)), scopes, false)
	}
}

// Share scope normalization between file filtering and directory pruning so
// missing, unsafe and symlink-escaped seeds retain the same closed behavior.
func normalizedScopePaths(root string, seeds []string) []string {
	scopes := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		seed = strings.TrimSpace(seed)
		if seed == "" {
			continue
		}
		normalized := filepath.ToSlash(filepath.Clean(seed))
		if normalized == "." {
			scopes = append(scopes, "")
			continue
		}
		absolute, err := resolvePath(root, normalized)
		if err != nil {
			continue
		}
		info, err := os.Stat(absolute)
		if err != nil {
			continue
		}
		if info.IsDir() {
			scopes = append(scopes, strings.TrimSuffix(normalized, "/"))
			continue
		}
		scopes = append(scopes, normalized)
	}
	return scopes
}

func pathIntersectsScopes(path string, scopes []string, directory bool) bool {
	for _, scope := range scopes {
		if scope == "" || path == scope || strings.HasPrefix(path, scope+"/") {
			return true
		}
		// Ancestors must stay traversable to reach nested directories/files.
		if directory && strings.HasPrefix(scope, path+"/") {
			return true
		}
	}
	return false
}

func loadSmartDocuments(root string, filePaths []string) []smartDocument {
	documents := make([]smartDocument, 0, len(filePaths))
	for _, path := range filePaths {
		absolute, err := resolvePath(root, path)
		if err != nil {
			continue
		}
		info, err := os.Stat(absolute)
		if err != nil || info.IsDir() || info.Size() > 2<<20 {
			continue
		}
		content, err := os.ReadFile(absolute)
		if err != nil || !utf8.Valid(content) {
			continue
		}
		text := string(content)
		documents = append(documents, smartDocument{Path: path, Content: text, Title: documentTitle(path, text), SHA256: digest(content)})
	}
	return documents
}

func resolvePath(root, path string) (string, error) {
	absolute, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if absolute != rootAbs && !strings.HasPrefix(absolute, rootAbs+string(filepath.Separator)) {
		return "", os.ErrPermission
	}
	return absolute, nil
}

func exactRecall(query string, documents []smartDocument) []smartCandidate {
	query = strings.TrimSpace(query)
	queryLower := strings.ToLower(query)
	normalizedQuery := normalizeIdentifier(query)
	result := make([]smartCandidate, 0)
	for _, document := range documents {
		candidate := smartCandidate{Path: document.Path, Sources: map[string]bool{"exact": true}, Matches: map[string]bool{}}
		pathLower := strings.ToLower(document.Path)
		pathNormalized := normalizeIdentifier(document.Path)
		titleLower := strings.ToLower(document.Title)
		titleNormalized := normalizeIdentifier(document.Title)

		if queryLower != "" {
			if line, ok := firstSmartMatchingLine(document.Content, queryLower, normalizedQuery); ok {
				candidate.Score += 1_000_000
				candidate.Matches[query] = true
				candidate.BestLine, candidate.HasBestLine = line, true
				candidate.WindowScore = 100_000
				candidate.MatchReason = "lexical: exact_phrase"
				candidate.ReasonPriority = 5
			}
		}
		pathOrTitle := (queryLower != "" && (strings.Contains(pathLower, queryLower) || strings.Contains(titleLower, queryLower))) ||
			(normalizedQuery != "" && (strings.Contains(pathNormalized, normalizedQuery) || strings.Contains(titleNormalized, normalizedQuery)))
		if pathOrTitle {
			candidate.Score += 200_000
			candidate.Matches[query] = true
			if candidate.ReasonPriority < 2 {
				candidate.MatchReason = "heuristic: title_or_path"
				candidate.ReasonPriority = 2
			}
		}
		if candidate.Score > 0 {
			result = append(result, candidate)
		}
	}
	return result
}

func tokenRecall(analysis QueryAnalysis, documents []smartDocument) []smartCandidate {
	result := make([]smartCandidate, 0)
	technical := make(map[string]bool, len(analysis.TechnicalTerms))
	for _, term := range analysis.TechnicalTerms {
		technical[term] = true
	}
	for _, document := range documents {
		candidate := smartCandidate{Path: document.Path, Sources: map[string]bool{"token": true}, Matches: map[string]bool{}}
		title := strings.ToLower(document.Title)
		path := normalizeIdentifier(document.Path)
		lines := strings.Split(document.Content, "\n")
		lineScores := make([]int, len(lines))
		cjkPhraseMatches := 0
		identifierMatches := 0
		titlePathMatches := 0
		lexicalMatches := 0
		repeatBonus := 0

		for _, phrase := range analysis.Phrases {
			lower := strings.ToLower(phrase)
			normalized := normalizeIdentifier(phrase)
			matchedTitlePath := strings.Contains(title, lower) || (normalized != "" && strings.Contains(path, normalized))
			bodyCount := 0
			for lineIndex, line := range lines {
				lineLower := strings.ToLower(line)
				if strings.Contains(lineLower, lower) || (normalized != "" && strings.Contains(normalizeIdentifier(line), normalized)) {
					bodyCount++
					lineScores[lineIndex] += 120
				}
			}
			if matchedTitlePath || bodyCount > 0 {
				candidate.Matches[phrase] = true
				if containsHan(phrase) {
					cjkPhraseMatches++
				}
			}
			if matchedTitlePath {
				titlePathMatches++
			}
			repeatBonus += minInt(bodyCount, 3) * 20
		}

		for _, token := range uniqueSmartTerms(analysis.Tokens, analysis.TechnicalTerms) {
			lower := strings.ToLower(token)
			normalized := normalizeIdentifier(token)
			matchedTitlePath := strings.Contains(title, lower) || (normalized != "" && strings.Contains(path, normalized))
			bodyCount := 0
			for lineIndex, line := range lines {
				lineLower := strings.ToLower(line)
				matched := strings.Contains(lineLower, lower)
				if !matched && normalized != "" {
					matched = strings.Contains(normalizeIdentifier(line), normalized)
				}
				if !matched {
					continue
				}
				bodyCount++
				switch {
				case technical[token]:
					lineScores[lineIndex] += 70
				case containsHan(token):
					lineScores[lineIndex] += 15
				default:
					lineScores[lineIndex] += 25
				}
			}
			if matchedTitlePath || bodyCount > 0 {
				candidate.Matches[token] = true
			}
			if matchedTitlePath {
				titlePathMatches++
			}
			if bodyCount > 0 {
				if technical[token] {
					identifierMatches++
				} else {
					lexicalMatches++
				}
			}
			repeatBonus += minInt(bodyCount, 3) * 5
		}

		bestLine, bestLineScore := bestSmartLine(lineScores)
		if bestLineScore > 0 {
			candidate.BestLine, candidate.HasBestLine = bestLine, true
			candidate.WindowScore = bestLineScore
		}
		switch {
		case cjkPhraseMatches > 0:
			candidate.Score = 500_000 + cjkPhraseMatches*4_000 + titlePathMatches*200 + lexicalMatches*25 + identifierMatches*100 + repeatBonus + bestLineScore
			candidate.MatchReason = "heuristic: cjk_term_coverage"
			candidate.ReasonPriority = 4
		case identifierMatches > 0:
			candidate.Score = 300_000 + identifierMatches*4_000 + titlePathMatches*200 + lexicalMatches*25 + repeatBonus + bestLineScore
			candidate.MatchReason = "lexical: identifier_exact"
			candidate.ReasonPriority = 3
		case titlePathMatches > 0:
			candidate.Score = 200_000 + titlePathMatches*2_000 + lexicalMatches*25 + repeatBonus + bestLineScore
			candidate.MatchReason = "heuristic: title_or_path"
			candidate.ReasonPriority = 2
		case lexicalMatches > 0:
			candidate.Score = 100_000 + lexicalMatches*500 + repeatBonus + bestLineScore
			candidate.MatchReason = "lexical: token_match"
			candidate.ReasonPriority = 1
		}
		if candidate.Score > 0 {
			result = append(result, candidate)
		}
	}
	return result
}

func mergeSmartCandidate(merged map[string]*smartCandidate, candidate smartCandidate) {
	current, ok := merged[candidate.Path]
	if !ok {
		copyCandidate := candidate
		copyCandidate.Score = 0
		copyCandidate.Sources = map[string]bool{}
		copyCandidate.Matches = map[string]bool{}
		copyCandidate.MatchReason = ""
		copyCandidate.ReasonPriority = 0
		copyCandidate.WindowScore = 0
		copyCandidate.HasBestLine = false
		merged[candidate.Path] = &copyCandidate
		current = &copyCandidate
	}
	current.Score += candidate.Score
	for source := range candidate.Sources {
		current.Sources[source] = true
	}
	for match := range candidate.Matches {
		current.Matches[match] = true
	}
	if candidate.ReasonPriority > current.ReasonPriority {
		current.MatchReason = candidate.MatchReason
		current.ReasonPriority = candidate.ReasonPriority
	}
	if candidate.HasBestLine && (!current.HasBestLine || candidate.WindowScore > current.WindowScore || (candidate.WindowScore == current.WindowScore && candidate.BestLine < current.BestLine)) {
		current.BestLine = candidate.BestLine
		current.HasBestLine = true
		current.WindowScore = candidate.WindowScore
	}
}

func smartContextWindow(candidate *smartCandidate, contextBefore, contextAfter int) (int, int) {
	if !candidate.HasBestLine {
		return 0, 120
	}
	if contextBefore < 0 {
		contextBefore = 0
	}
	if contextAfter < 0 {
		contextAfter = 0
	}
	if contextBefore == 0 && contextAfter == 0 {
		return candidate.BestLine, 120
	}
	offset := candidate.BestLine - contextBefore
	if offset < 0 {
		offset = 0
	}
	limit := contextBefore + 1 + contextAfter
	if limit <= 0 {
		limit = 1
	}
	return offset, limit
}

func firstSmartMatchingLine(content, queryLower, normalizedQuery string) (int, bool) {
	for index, line := range strings.Split(content, "\n") {
		lineLower := strings.ToLower(line)
		if queryLower != "" && strings.Contains(lineLower, queryLower) {
			return index, true
		}
		if normalizedQuery != "" && strings.Contains(normalizeIdentifier(line), normalizedQuery) {
			return index, true
		}
	}
	return 0, false
}

func bestSmartLine(scores []int) (int, int) {
	bestLine, bestScore := 0, 0
	for index, score := range scores {
		if score > bestScore {
			bestLine, bestScore = index, score
		}
	}
	return bestLine, bestScore
}

func uniqueSmartTerms(groups ...[]string) []string {
	seen := map[string]bool{}
	result := make([]string, 0)
	for _, group := range groups {
		for _, term := range group {
			if term == "" || seen[term] {
				continue
			}
			seen[term] = true
			result = append(result, term)
		}
	}
	return result
}

func containsHan(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func smartSources(sources map[string]bool) []string {
	result := make([]string, 0, 2)
	for _, source := range []string{"exact", "token"} {
		if sources[source] {
			result = append(result, source)
		}
	}
	return result
}

func smartMatches(matches map[string]bool) []string {
	result := make([]string, 0, len(matches))
	for match := range matches {
		result = append(result, match)
	}
	sort.Strings(result)
	return result
}

func smartMetadata(path string) map[string]any {
	ext := strings.ToLower(filepath.Ext(path))
	language := strings.TrimPrefix(ext, ".")
	typ := "source"
	if ext == ".md" || ext == ".markdown" {
		typ = "markdown"
		if strings.Contains(strings.ToLower(path), "/prd/") {
			typ = "prd"
		}
	}
	return map[string]any{"type": typ, "language": language}
}

func documentTitle(path, content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

func normalizeIdentifier(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
