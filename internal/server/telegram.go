package server

import "github.com/metasequoiaime/MSIME-Backend/internal/account"

// Telegram notice channel (unit U8).

// noticeBroadcaster returns what delivers published notices to admin.telegram, or nil when no channel is configured.
func (s *Server) noticeBroadcaster() account.NoticeBroadcaster {
	return nil
}
