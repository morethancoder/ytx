package channel

import "github.com/morethancoder/ytx/internal/ops"

func init() {
	ops.Handle("auth.login", login)
	ops.Handle("auth.logout", logout)
	ops.Handle("mine.videos", mineVideos)
	ops.Handle("video.upload", upload)
	ops.Handle("video.edit", edit)
	ops.Handle("video.thumbnail", thumbnail)
	ops.Handle("video.rate", rate)
	ops.Handle("playlist.create", playlistCreate)
	ops.Handle("playlist.add", playlistAdd)
	ops.Handle("playlist.remove", playlistRemove)
	ops.Handle("comment.post", commentPost)
	ops.Handle("comment.reply", commentReply)
	ops.Handle("comment.moderate", commentModerate)
	ops.Handle("captions.list", captionsList)
	ops.Handle("captions.upload", captionsUpload)
	ops.Handle("me.channel", meChannel)
	ops.Handle("me.videos", meVideos)
	ops.Handle("me.retention", meRetention)
	ops.Handle("me.learn", meLearn)
}
