// Package model holds the shapes every ytx command returns, whatever the
// source (keyless InnerTube or the official API). Fields a source can't fill
// stay zero and are left out of the JSON.
package model

import "time"

// Video is one YouTube video as a search result, a channel upload or a full lookup.
type Video struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	ChannelID   string    `json:"channel_id,omitempty"`
	ChannelName string    `json:"channel_name,omitempty"`
	Views       int64     `json:"views"`
	Likes       int64     `json:"likes,omitempty"`
	Comments    int64     `json:"comments,omitempty"`
	DurationS   int       `json:"duration_s,omitempty"`
	Published   time.Time `json:"published,omitempty"`
	// PublishedText is YouTube's own relative date ("3 weeks ago") when no exact date is known.
	PublishedText string   `json:"published_text,omitempty"`
	AgeDays       int      `json:"age_days,omitempty"`
	Thumbnail     string   `json:"thumbnail,omitempty"` // the largest thumbnail URL
	Description   string   `json:"description,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	IsShort       bool     `json:"is_short,omitempty"`
	IsLive        bool     `json:"is_live,omitempty"`
	// Outlier is views ÷ the median views of the channel's recent uploads (set by research commands).
	Outlier float64 `json:"outlier,omitempty"`
}

// Channel is one YouTube channel.
type Channel struct {
	ID          string `json:"id"`
	Handle      string `json:"handle,omitempty"` // "@name"
	URL         string `json:"url"`
	Name        string `json:"name"`
	Subscribers int64  `json:"subscribers,omitempty"`
	VideoCount  int64  `json:"video_count,omitempty"`
	ViewCount   int64  `json:"view_count,omitempty"`
	Description string `json:"description,omitempty"`
	Thumbnail   string `json:"thumbnail,omitempty"`
	// Recent is the channel's latest uploads, newest first (filled by `channel`).
	Recent []Video `json:"recent,omitempty"`
	// MedianViews is the median of Recent's views, the base for outlier scores.
	MedianViews int64 `json:"median_views,omitempty"`
	// TooNew is true when too few of Recent are old enough to judge, so there is no median.
	TooNew bool `json:"too_new,omitempty"`
}

// Comment is one top-level comment or reply.
type Comment struct {
	ID         string    `json:"id"`
	Author     string    `json:"author"`
	Text       string    `json:"text"`
	Likes      int64     `json:"likes"`
	Replies    int64     `json:"replies,omitempty"`
	Published  time.Time `json:"published,omitempty"`
	When       string    `json:"when,omitempty"` // YouTube's relative date when no exact one is known
	IsQuestion bool      `json:"is_question,omitempty"`
	ParentID   string    `json:"parent_id,omitempty"`
}

// Segment is one timed line of a transcript.
type Segment struct {
	StartS float64 `json:"start_s"`
	DurS   float64 `json:"dur_s"`
	Text   string  `json:"text"`
}

// Transcript is a video's captions as text.
type Transcript struct {
	VideoID   string    `json:"video_id"`
	Language  string    `json:"language"`
	Generated bool      `json:"auto_generated"`
	Segments  []Segment `json:"segments"`
	Text      string    `json:"text"` // all segments joined, for reading and searching
	Words     int       `json:"words"`
}

// Playlist is one playlist.
type Playlist struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Privacy     string `json:"privacy,omitempty"`
	ItemCount   int64  `json:"item_count,omitempty"`
}
