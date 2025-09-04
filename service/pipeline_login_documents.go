package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

const (
	DocumentStorageCollection    = "GameDocuments"
	StorageCollectionChannelInfo = "ChannelInfo"
)

var (
	channelInfoIndex = StorableIndexMeta{
		Name:       "ChannelInfoIndex",
		Collection: StorageCollectionChannelInfo,
		Fields:     []string{"channeluuid"},
		MaxEntries: 200,
		IndexOnly:  false,
	}
)

func (p *Pipeline) documentRequest(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
	request := in.(*evr.DocumentRequest)

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}

	var document evr.Document
	var err error
	switch request.Type {
	case "eula":

		if !params.IsVR() {

			eulaVersion := params.profile.LegalConsents.EulaVersion
			gaVersion := params.profile.LegalConsents.GameAdminVersion
			document = evr.NewEULADocument(int(eulaVersion), int(gaVersion), request.Language, "https://github.com/EchoTools", "Blank EULA for NoVR clients. You should only see this once.")
			return session.SendEVR(Envelope{
				ServiceType: ServiceTypeLogin,
				Messages: []evr.Message{
					evr.NewDocumentSuccess(document),
				},
				State: RequireStateUnrequired,
			})
		}

		document, err = p.generateEULA(ctx, logger, request.Language)
		if err != nil {
			return fmt.Errorf("failed to get eula document: %w", err)
		}
		return session.SendEVR(Envelope{
			ServiceType: ServiceTypeLogin,
			Messages: []evr.Message{
				evr.NewDocumentSuccess(document),
			},
			State: RequireStateUnrequired,
		})

	default:
		return fmt.Errorf("unknown document: %s,%s", request.Language, request.Type)
	}
}

func (p *Pipeline) generateEULA(ctx context.Context, logger *zap.Logger, language string) (evr.EULADocument, error) {
	// Retrieve the contents from storage
	key := fmt.Sprintf("eula,%s", language)
	document := evr.DefaultEULADocument(language)

	var ts time.Time

	objs, _, err := p.nk.StorageIndexList(ctx, SystemUserID, DocumentStorageCollection, fmt.Sprintf("+key:%s", key), 1, nil, "")

	if err != nil {
		return document, fmt.Errorf("failed to read EULA: %w", err)
	} else if len(objs.Objects) > 0 {
		if err := json.Unmarshal([]byte(objs.Objects[0].Value), &document); err != nil {
			return document, fmt.Errorf("failed to unmarshal EULA: %w", err)
		}
		ts = objs.Objects[0].UpdateTime.AsTime().UTC()
	} else {
		// If the document doesn't exist, store the object
		jsonBytes, err := json.Marshal(document)
		if err != nil {
			return document, fmt.Errorf("failed to marshal EULA: %w", err)
		}

		if _, err = p.nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			Collection:      DocumentStorageCollection,
			Key:             key,
			Value:           string(jsonBytes),
			PermissionRead:  0,
			PermissionWrite: 0,
		}}); err != nil {
			return document, fmt.Errorf("failed to write EULA: %w", err)
		}
		ts = time.Now().UTC()
	}

	msg := document.Text
	maxLineCount := 7
	maxLineLength := 28
	// Split the message by newlines

	// trim the final newline
	msg = strings.TrimRight(msg, "\n")

	// Limit the EULA to 7 lines, and add '...' to the end of any line that is too long.
	lines := strings.Split(msg, "\n")
	if len(lines) > maxLineCount {
		logger.Warn("EULA too long", zap.Int("lineCount", len(lines)))
		lines = lines[:maxLineCount]
		lines = append(lines, "...")
	}

	// Cut lines at 18 characters
	for i, line := range lines {
		if len(line) > maxLineLength {
			logger.Warn("EULA line too long", zap.String("line", line), zap.Int("length", len(line)))
			lines[i] = line[:maxLineLength-3] + "..."
		}
	}

	msg = strings.Join(lines, "\n") + "\n"

	document.Version = ts.Unix()
	document.VersionGameAdmin = ts.Unix()

	document.Text = msg
	return document, nil
}

func (p *Pipeline) channelInfoRequest(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
	_ = in.(*evr.ChannelInfoRequest)

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}

	groupID := params.profile.GetActiveGroupID()

	if groupID.IsNil() {
		return fmt.Errorf("active group is nil")
	}

	// Use the Index
	query := "+key:%s" + groupID.String()
	objs, _, err := p.nk.StorageIndexList(ctx, SystemUserID, channelInfoIndex.Name, query, 1, nil, "")
	if err != nil {
		return fmt.Errorf("failed to query channel info: %w", err)
	}
	if len(objs.Objects) > 0 {
		// send the document to the client
		return session.SendEVR(Envelope{
			ServiceType: ServiceTypeLogin,
			Messages: []evr.Message{
				&evr.ChannelInfoResponse{
					ChannelInfo: json.RawMessage(objs.Objects[0].GetValue()),
				},
			},
			State: RequireStateUnrequired,
		})
	}

	// Get the guild group for the active group ID.
	g, ok := params.guildGroups[groupID.String()]
	if !ok {
		return fmt.Errorf("guild group not found: %s", groupID.String())
	}

	resource := evr.NewChannelInfoResource()

	resource.Groups = [4]evr.ChannelGroup{}
	for i := range resource.Groups {
		resource.Groups[i] = evr.ChannelGroup{
			ChannelUuid:  strings.ToUpper(g.ID().String()),
			Name:         g.Name(),
			Description:  g.Description(),
			Rules:        g.Description() + "\n" + g.State.RulesText,
			RulesVersion: 1,
			Link:         fmt.Sprintf("https://discord.gg/channel/%s", g.GuildID),
			Priority:     uint64(i),
			RAD:          true,
		}
	}

	message := evr.NewSNSChannelInfoResponse(resource)
	data, err := json.Marshal(resource)
	if err != nil {
		return fmt.Errorf("failed to marshal channel info: %w", err)
	}
	// Store the document in the storage
	p.nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      StorageCollectionChannelInfo,
			Key:             groupID.String(),
			UserID:          SystemUserID,
			Value:           string(data),
			PermissionRead:  2,
			PermissionWrite: 0,
		},
	})
	// send the document to the client
	return session.SendEVR(Envelope{
		ServiceType: ServiceTypeLogin,
		Messages: []evr.Message{
			message,
		},
		State: RequireStateUnrequired,
	})
}
