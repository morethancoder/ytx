package keyless

import (
	"context"
	"strings"

	"github.com/morethancoder/ytx/internal/apperr"
	"github.com/morethancoder/ytx/internal/model"
	"github.com/morethancoder/ytx/internal/yt"
)

// maxCommentPages caps continuation requests for one call.
const maxCommentPages = 60

// Comments reads top-level comments, top or newest. Comment data now arrives as
// entity mutations (frameworkUpdates.entityBatchUpdate, commentEntityPayload)
// that the thread list points at by key, so both are read and joined by id.
func (c *Client) Comments(ctx context.Context, videoID string, o yt.CommentOpts) ([]model.Comment, error) {
	limit := clampLimit(o.Limit, 20)
	next, err := c.post(ctx, request{endpoint: "next", client: webClient("US"), body: obj{"videoId": videoID}})
	if err != nil {
		return nil, err
	}
	token, err := commentsToken(next, o.Order)
	if err != nil {
		return nil, err
	}
	out := []model.Comment{}
	seen := map[string]bool{}
	for page := 0; page < maxCommentPages && token != "" && len(out) < limit; page++ {
		resp, err := c.post(ctx, request{endpoint: "next", client: webClient("US"), body: obj{"continuation": token}})
		if err != nil {
			return nil, err
		}
		for _, cm := range parseComments(resp) {
			if cm.ID != "" && !seen[cm.ID] {
				seen[cm.ID] = true
				out = append(out, cm)
			}
		}
		token = commentsNextToken(resp)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// commentsToken finds the continuation that starts the comment list in a /next
// answer, or says why there is none (comments off, video gone, unknown layout).
func commentsToken(next obj, order string) (string, error) {
	if findFirst(next["contents"], "backgroundPromoRenderer") != nil {
		return "", apperr.New(apperr.NotFound, "video not found: it is not available")
	}
	if order == "new" {
		for _, item := range findAll(next, "subMenuItems") {
			for _, it := range asList(item) {
				if strings.EqualFold(str(get(it, "title")), "Newest") {
					if t := str(get(it, "serviceEndpoint", "continuationCommand", "token")); t != "" {
						return t, nil
					}
				}
			}
		}
	}
	var section obj
	for _, s := range findAll(next["contents"], "itemSectionRenderer") {
		if str(get(s, "sectionIdentifier")) == "comment-item-section" {
			section = asMap(s)
			break
		}
	}
	if section == nil {
		if next["contents"] == nil {
			return "", layoutErr("watch page answer has no contents")
		}
		return "", layoutErr("watch page has no comment section")
	}
	if m := findFirst(section, "messageRenderer"); m != nil &&
		strings.Contains(strings.ToLower(text(get(m, "text"))), "turned off") {
		return "", apperr.New(apperr.CommentsDisabled, "comments are turned off for this video")
	}
	if tok := nextToken(section); tok != "" {
		return tok, nil
	}
	if m := findFirst(section, "messageRenderer"); m != nil {
		// "Comments are limited" or similar notices carry no list to read.
		return "", apperr.New(apperr.CommentsDisabled, "comments are not available: %s", strings.TrimSpace(text(get(m, "text"))))
	}
	return "", layoutErr("comment section has no continuation")
}

// commentsNextToken is the token for the next page of comments: the last item
// of the continuation list, when it is a continuation item.
func commentsNextToken(resp obj) string {
	tok := ""
	for _, ci := range findAll(resp, "continuationItems") {
		items := asList(ci)
		if len(items) == 0 {
			continue
		}
		if t := str(get(items[len(items)-1], "continuationItemRenderer", "continuationEndpoint", "continuationCommand", "token")); t != "" {
			tok = t
		}
	}
	return tok
}

// parseComments reads one page of comment threads.
func parseComments(resp obj) []model.Comment {
	entities := map[string]obj{}
	for _, m := range asList(get(resp, "frameworkUpdates", "entityBatchUpdate", "mutations")) {
		if p := asMap(get(m, "payload", "commentEntityPayload")); p != nil {
			if id := str(get(p, "properties", "commentId")); id != "" {
				entities[id] = p
			}
		}
	}
	var out []model.Comment
	for _, ci := range findAll(resp, "continuationItems") {
		for _, item := range asList(ci) {
			thread := asMap(get(item, "commentThreadRenderer"))
			if thread == nil {
				continue
			}
			var cm model.Comment
			if vm := asMap(get(thread, "commentViewModel", "commentViewModel")); vm != nil {
				id := str(vm["commentId"])
				p, ok := entities[id]
				if !ok {
					continue
				}
				cm = commentFromEntity(p)
			} else if r := asMap(get(thread, "comment", "commentRenderer")); r != nil {
				cm = commentFromRenderer(r)
			} else {
				continue
			}
			if cm.Replies == 0 {
				cm.Replies = parseCount(strings.TrimSuffix(text(get(thread, "replies", "commentRepliesRenderer", "viewReplies", "buttonRenderer", "text")), " replies"))
			}
			out = append(out, cm)
		}
	}
	return out
}

func cleanWhen(s string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "(edited)"))
}

func commentFromEntity(p obj) model.Comment {
	props := asMap(p["properties"])
	tb := asMap(p["toolbar"])
	likes := orDefault(str(tb["likeCountNotliked"]), str(tb["likeCountLiked"]))
	return model.Comment{
		ID:      str(props["commentId"]),
		Author:  orDefault(str(get(p, "author", "displayName")), str(props["authorButtonA11y"])),
		Text:    text(props["content"]),
		Likes:   parseCount(likes),
		Replies: parseCount(str(tb["replyCount"])),
		When:    cleanWhen(str(props["publishedTime"])),
	}
}

// commentFromRenderer reads the older commentRenderer layout, which some
// requests still get.
func commentFromRenderer(r obj) model.Comment {
	return model.Comment{
		ID:      str(r["commentId"]),
		Author:  text(r["authorText"]),
		Text:    text(r["contentText"]),
		Likes:   parseCount(orDefault(text(r["voteCount"]), str(r["likeCount"]))),
		Replies: int64Of(r["replyCount"]),
		When:    cleanWhen(text(r["publishedTimeText"])),
	}
}

func int64Of(v any) int64 {
	if f, ok := v.(float64); ok {
		return int64(f)
	}
	return 0
}
