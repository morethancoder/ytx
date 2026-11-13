package ops

func p(name, typ, help string) Param { return Param{Name: name, Type: typ, Help: help} }
func pos(name, help string) Param {
	return Param{Name: name, Type: TString, Required: true, Positional: true, Help: help}
}
func def(pr Param, v any) Param { pr.Default = v; return pr }

var (
	pQuery   = pos("query", "what to search for")
	pVideo   = pos("video", "video id or URL")
	pChannel = pos("channel", "channel id, @handle or URL")
	pLimit   = def(p("limit", TInt, "how many results"), 20)
	pRegion  = def(p("region", TString, "two-letter country code"), "US")
	pLang    = p("lang", TString, "language code, e.g. en or ar (default: YouTube's choice)")
	pOut     = p("out", TString, "folder to write into (default: research_dir from config, else the current folder)")
)

// Quota costs are the official YouTube Data API's units per call (see
// https://developers.google.com/youtube/v3/determine_quota_cost). Keep them in step with the docs.

// All is every ytx operation.
var All = []Op{
	// ---- reading YouTube (keyless; the official API when YOUTUBE_API_KEY is set)
	{Name: "search", Command: "search", Short: "Search videos: title, channel, views, age, length, thumbnail (plus 1 of the 100 daily search calls)", Kind: Read, Auth: AuthNone, Quota: 1, Cache: "24h",
		Params: []Param{pQuery, pLimit,
			def(p("type", TString, "video, short or any"), "video"),
			def(p("order", TString, "relevance, views or date"), "relevance"),
			p("since", TInt, "only videos from the last N days"),
			pLang, pRegion}},
	{Name: "video.get", Command: "video", Short: "One video's full details", Kind: Read, Auth: AuthNone, Quota: 1, Cache: "1h",
		Params: []Param{pVideo}},
	{Name: "channel.get", Command: "channel", Short: "A channel and its recent uploads, with the median views of those uploads", Kind: Read, Auth: AuthNone, Quota: 3, Cache: "1h",
		Params: []Param{pChannel, def(p("limit", TInt, "how many recent uploads"), 30),
			p("shorts", TBool, "list Shorts instead of long videos")}},
	{Name: "comments.list", Command: "comments", Short: "A video's comments, most liked first, with questions marked", Kind: Read, Auth: AuthNone, Quota: 1, Cache: "6h",
		Params: []Param{pVideo, def(p("limit", TInt, "how many comments"), 100),
			def(p("order", TString, "top or new"), "top"),
			p("questions", TBool, "only comments that ask something")}},
	{Name: "transcript.get", Command: "transcript", Short: "A video's transcript (captions as text, with timings)", Kind: Read, Auth: AuthNone, Cache: "720h",
		Params: []Param{pVideo, pLang, p("text", TBool, "return only the joined text, no segments")}},
	{Name: "suggest", Command: "suggest", Short: "YouTube search autocomplete for a phrase (a demand signal)", Kind: Read, Auth: AuthNone, Cache: "24h",
		Params: []Param{pQuery, pLang, pRegion, p("expand", TBool, "also ask for the phrase followed by a..z and common question words")}},
	{Name: "trending", Command: "trending", Short: "What is trending now in a region", Kind: Read, Auth: AuthNone, Quota: 1, Cache: "1h",
		Params: []Param{pRegion, def(p("category", TString, "now, music, gaming or movies"), "now"), pLimit}},
	{Name: "playlist.get", Command: "playlist", Short: "A playlist and its videos", Kind: Read, Auth: AuthNone, Quota: 2, Cache: "1h",
		Params: []Param{pos("playlist", "playlist id or URL"), def(p("limit", TInt, "how many videos"), 50)}},

	// ---- research (built on the reads above)
	{Name: "research.outliers", Command: "outliers", Short: "Videos on a topic ranked by views ÷ their channel's median views", Kind: Read, Auth: AuthNone,
		Params: []Param{pQuery, pLimit, def(p("recent", TInt, "uploads per channel used for the median"), 30),
			p("min", TString, "only outliers at or above this score, e.g. 2"), p("since", TInt, "only videos from the last N days"), pLang, pRegion}},
	{Name: "research.gaps", Command: "gaps", Short: "What viewers ask in the top videos' comments that their transcripts don't answer", Kind: Read, Auth: AuthNone,
		Params: []Param{pQuery, def(p("top", TInt, "how many top videos to read"), 5), pLang, pRegion}},
	{Name: "research.run", Command: "research", Short: "Search, outliers, autocomplete and top comments, written as one Markdown note", Kind: Local, Auth: AuthNone,
		Params: []Param{pQuery, pOut, def(p("top", TInt, "videos whose comments and transcripts are read"), 3),
			p("name", TString, "note file name (default: the topic)"), pLang, pRegion}},

	// ---- your channel (official API, after `ytx login`)
	{Name: "mine.videos", Command: "my videos", Short: "Your uploads, private and scheduled ones included", Kind: Read, Auth: AuthLogin, Quota: 3,
		Params: []Param{def(p("limit", TInt, "how many"), 50)}},
	{Name: "video.upload", Command: "upload", Short: "Upload a video (private unless --privacy says otherwise) (uses 1 of the 100 daily upload calls)", Kind: Write, Auth: AuthLogin, Quota: 0,
		Params: []Param{{Name: "file", Type: TFile, Required: true, Positional: true, Help: "video file"},
			{Name: "title", Type: TString, Required: true, Help: "title"},
			p("description", TString, "description (or @file.txt)"), p("tags", TList, "tags, comma-separated"),
			def(p("privacy", TString, "private, unlisted or public"), "private"),
			p("publish-at", TString, "schedule: RFC 3339 time (needs privacy private)"),
			p("thumbnail", TFile, "thumbnail image to set after upload"), p("playlist", TString, "playlist to add it to"),
			p("category", TString, "category id"), p("kids", TBool, "made for kids"), pLang}},
	{Name: "video.edit", Command: "edit", Short: "Change a video's title, description, tags, privacy or category", Kind: Write, Auth: AuthLogin, Quota: 51,
		Params: []Param{pVideo, p("title", TString, "new title"), p("description", TString, "new description (or @file.txt)"),
			p("tags", TList, "new tags, comma-separated"), p("privacy", TString, "private, unlisted or public"),
			p("publish-at", TString, "schedule: RFC 3339 time"), p("category", TString, "category id")}},
	{Name: "video.thumbnail", Command: "thumbnail", Short: "Set a video's thumbnail", Kind: Write, Auth: AuthLogin, Quota: 50,
		Params: []Param{pVideo, {Name: "file", Type: TFile, Required: true, Positional: true, Help: "image (JPG or PNG, under 2 MB, 1280x720 best)"}}},
	{Name: "video.rate", Command: "rate", Short: "Like, dislike or clear your rating on a video", Kind: Write, Auth: AuthLogin, Quota: 50,
		Params: []Param{pVideo, {Name: "rating", Type: TString, Required: true, Positional: true, Help: "like, dislike or none"}}},
	{Name: "playlist.create", Command: "playlist create", Short: "Create a playlist", Kind: Write, Auth: AuthLogin, Quota: 50,
		Params: []Param{pos("title", "playlist title"), p("description", TString, "description"), def(p("privacy", TString, "private, unlisted or public"), "private")}},
	{Name: "playlist.add", Command: "playlist add", Short: "Add a video to a playlist", Kind: Write, Auth: AuthLogin, Quota: 50,
		Params: []Param{pos("playlist", "playlist id or URL"), pVideo, p("position", TInt, "0-based position (default: end)")}},
	{Name: "playlist.remove", Command: "playlist remove", Short: "Remove a video from a playlist", Kind: Write, Auth: AuthLogin, Quota: 51,
		Params: []Param{pos("playlist", "playlist id or URL"), pVideo}},
	{Name: "comment.post", Command: "comment post", Short: "Post a top-level comment on a video", Kind: Write, Auth: AuthLogin, Quota: 50,
		Params: []Param{pVideo, pos("text", "comment text")}},
	{Name: "comment.reply", Command: "comment reply", Short: "Reply to a comment", Kind: Write, Auth: AuthLogin, Quota: 50,
		Params: []Param{pos("comment", "comment id"), pos("text", "reply text")}},
	{Name: "comment.moderate", Command: "comment moderate", Short: "Publish, hold or reject a comment on your video", Kind: Write, Auth: AuthLogin, Quota: 50,
		Params: []Param{pos("comment", "comment id"), {Name: "status", Type: TString, Required: true, Positional: true, Help: "published, heldForReview or rejected"},
			p("ban", TBool, "also ban the author (with rejected)")}},
	{Name: "captions.list", Command: "captions", Short: "Caption tracks on your video", Kind: Read, Auth: AuthLogin, Quota: 50,
		Params: []Param{pVideo}},
	{Name: "captions.upload", Command: "captions upload", Short: "Upload a caption file (SRT or VTT) to your video", Kind: Write, Auth: AuthLogin, Quota: 400,
		Params: []Param{pVideo, {Name: "file", Type: TFile, Required: true, Positional: true, Help: "SRT or VTT file"},
			{Name: "lang", Type: TString, Required: true, Help: "caption language code"}, p("name", TString, "track name"), p("draft", TBool, "upload as a draft")}},

	// ---- your analytics (YouTube Analytics / Reporting API, after `ytx login`)
	{Name: "me.channel", Command: "me", Short: "Your channel's last 28 days: views, watch time, subscribers", Kind: Read, Auth: AuthLogin,
		Params: []Param{def(p("days", TInt, "period length"), 28)}},
	{Name: "me.videos", Command: "me videos", Short: "Per video: views, watch time, average view duration and percentage (impressions and CTR are not in the API)", Kind: Read, Auth: AuthLogin,
		Params: []Param{def(p("days", TInt, "period length"), 90), def(p("limit", TInt, "how many videos"), 50)}},
	{Name: "me.retention", Command: "me retention", Short: "One video's audience retention curve", Kind: Read, Auth: AuthLogin,
		Params: []Param{pVideo}},
	{Name: "me.learn", Command: "me learn", Short: "A Markdown note comparing your best and worst videos", Kind: Local, Auth: AuthLogin,
		Params: []Param{pOut, def(p("days", TInt, "period length"), 365)}},

	// ---- packaging
	{Name: "feedcheck", Command: "feedcheck", Short: "Put your thumbnail and title among the top five competitors for a topic, as review sheets (needs the agd CLI to render; writes its input without it)", Kind: Local, Auth: AuthNone, Quota: 16,
		Params: []Param{pos("topic", "the topic whose top videos are your competitors"),
			{Name: "thumb", Type: TStrings, Required: true, Help: "your thumbnail (JPG, PNG or GIF); repeat for versions"},
			{Name: "title", Type: TStrings, Required: true, Help: "your title, up to 100 characters; repeat for versions (commas are fine)"},
			def(p("position", TInt, "where your video sits in the feed, 1 to 6"), 2),
			pOut, pLang, pRegion,
			def(p("theme", TString, "agd palette id: light, dark, oled, studio or ash"), "light"),
			p("duration", TString, "your video's length for the badge, e.g. 12:04 (no badge without it)"),
			p("channel", TString, "your channel name when not signed in (default: Your channel)"),
			p("avatar", TString, "your channel avatar: image path or URL (default: the initial)")}},

	// ---- files
	{Name: "download", Command: "download", Short: "Download a video or its audio with yt-dlp", Kind: Local, Auth: AuthLocal,
		Params: []Param{pVideo, def(p("format", TString, "video or audio"), "video"), p("height", TInt, "max video height, e.g. 1080"),
			p("out", TString, "folder to save into (default: the current folder)")}},

	// ---- setup
	{Name: "auth.login", Command: "login", Short: "Sign in with Google for your channel (opens the browser once)", Kind: Local, Auth: AuthLocal,
		Params: []Param{p("client", TFile, "OAuth client JSON from Google Cloud (needed the first time)")}},
	{Name: "auth.logout", Command: "logout", Short: "Forget the Google sign-in", Kind: Local, Auth: AuthLocal},
	{Name: "key.set", Command: "key set", Short: "Store a YouTube Data API key in the Keychain (reads it from stdin)", Kind: Local, Auth: AuthLocal},
	{Name: "key.remove", Command: "key remove", Short: "Remove the stored API key", Kind: Local, Auth: AuthLocal},
	{Name: "doctor", Command: "doctor", Short: "Which modes work: keyless, API key, signed in, yt-dlp", Kind: Read, Auth: AuthLocal},
	{Name: "quota", Command: "quota", Short: "Official API units used today and left", Kind: Read, Auth: AuthLocal},
	{Name: "config.set", Command: "config set", Short: "Set a config value (research_dir, region, lang, quota_limit)", Kind: Local, Auth: AuthLocal,
		Params: []Param{pos("key", "config key"), pos("value", "value")}},
	{Name: "config.show", Command: "config", Short: "Show the config and where it lives", Kind: Read, Auth: AuthLocal},
	{Name: "cache.clear", Command: "cache clear", Short: "Delete cached results", Kind: Local, Auth: AuthLocal},
}
