package service

const (
	ErrCodeGeneralError = 0

	ErrCodeUnknownAccount                        = 10001
	ErrCodeUnknownApplication                    = 10002
	ErrCodeUnknownChannel                        = 10003
	ErrCodeUnknownGuild                          = 10004
	ErrCodeUnknownIntegration                    = 10005
	ErrCodeUnknownInvite                         = 10006
	ErrCodeUnknownMember                         = 10007
	ErrCodeUnknownMessage                        = 10008
	ErrCodeUnknownOverwrite                      = 10009
	ErrCodeUnknownProvider                       = 10010
	ErrCodeUnknownRole                           = 10011
	ErrCodeUnknownToken                          = 10012
	ErrCodeUnknownUser                           = 10013
	ErrCodeUnknownEmoji                          = 10014
	ErrCodeUnknownWebhook                        = 10015
	ErrCodeUnknownWebhookService                 = 10016
	ErrCodeUnknownSession                        = 10020
	ErrCodeUnknownBan                            = 10026
	ErrCodeUnknownSKU                            = 10027
	ErrCodeUnknownStoreListing                   = 10028
	ErrCodeUnknownEntitlement                    = 10029
	ErrCodeUnknownBuild                          = 10030
	ErrCodeUnknownLobby                          = 10031
	ErrCodeUnknownBranch                         = 10032
	ErrCodeUnknownStoreDirectoryLayout           = 10033
	ErrCodeUnknownRedistributable                = 10036
	ErrCodeUnknownGiftCode                       = 10038
	ErrCodeUnknownStream                         = 10049
	ErrCodeUnknownPremiumServerSubscribeCooldown = 10050
	ErrCodeUnknownGuildTemplate                  = 10057
	ErrCodeUnknownDiscoveryCategory              = 10059
	ErrCodeUnknownSticker                        = 10060
	ErrCodeUnknownInteraction                    = 10062
	ErrCodeUnknownApplicationCommand             = 10063
	ErrCodeUnknownApplicationCommandPermissions  = 10066
	ErrCodeUnknownStageInstance                  = 10067
	ErrCodeUnknownGuildMemberVerificationForm    = 10068
	ErrCodeUnknownGuildWelcomeScreen             = 10069
	ErrCodeUnknownGuildScheduledEvent            = 10070
	ErrCodeUnknownGuildScheduledEventUser        = 10071
	ErrUnknownTag                                = 10087

	ErrCodeBotsCannotUseEndpoint                                            = 20001
	ErrCodeOnlyBotsCanUseEndpoint                                           = 20002
	ErrCodeExplicitContentCannotBeSentToTheDesiredRecipients                = 20009
	ErrCodeYouAreNotAuthorizedToPerformThisActionOnThisApplication          = 20012
	ErrCodeThisActionCannotBePerformedDueToSlowmodeRateLimit                = 20016
	ErrCodeOnlyTheOwnerOfThisAccountCanPerformThisAction                    = 20018
	ErrCodeMessageCannotBeEditedDueToAnnouncementRateLimits                 = 20022
	ErrCodeChannelHasHitWriteRateLimit                                      = 20028
	ErrCodeTheWriteActionYouArePerformingOnTheServerHasHitTheWriteRateLimit = 20029
	ErrCodeStageTopicContainsNotAllowedWordsForPublicStages                 = 20031
	ErrCodeGuildPremiumSubscriptionLevelTooLow                              = 20035

	ErrCodeMaximumGuildsReached                                     = 30001
	ErrCodeMaximumPinsReached                                       = 30003
	ErrCodeMaximumNumberOfRecipientsReached                         = 30004
	ErrCodeMaximumGuildRolesReached                                 = 30005
	ErrCodeMaximumNumberOfWebhooksReached                           = 30007
	ErrCodeMaximumNumberOfEmojisReached                             = 30008
	ErrCodeTooManyReactions                                         = 30010
	ErrCodeMaximumNumberOfGuildChannelsReached                      = 30013
	ErrCodeMaximumNumberOfAttachmentsInAMessageReached              = 30015
	ErrCodeMaximumNumberOfInvitesReached                            = 30016
	ErrCodeMaximumNumberOfAnimatedEmojisReached                     = 30018
	ErrCodeMaximumNumberOfServerMembersReached                      = 30019
	ErrCodeMaximumNumberOfGuildDiscoverySubcategoriesReached        = 30030
	ErrCodeGuildAlreadyHasATemplate                                 = 30031
	ErrCodeMaximumNumberOfThreadParticipantsReached                 = 30033
	ErrCodeMaximumNumberOfBansForNonGuildMembersHaveBeenExceeded    = 30035
	ErrCodeMaximumNumberOfBansFetchesHasBeenReached                 = 30037
	ErrCodeMaximumNumberOfUncompletedGuildScheduledEventsReached    = 30038
	ErrCodeMaximumNumberOfStickersReached                           = 30039
	ErrCodeMaximumNumberOfPruneRequestsHasBeenReached               = 30040
	ErrCodeMaximumNumberOfGuildWidgetSettingsUpdatesHasBeenReached  = 30042
	ErrCodeMaximumNumberOfEditsToMessagesOlderThanOneHourReached    = 30046
	ErrCodeMaximumNumberOfPinnedThreadsInForumChannelHasBeenReached = 30047
	ErrCodeMaximumNumberOfTagsInForumChannelHasBeenReached          = 30048

	ErrCodeUnauthorized                           = 40001
	ErrCodeActionRequiredVerifiedAccount          = 40002
	ErrCodeOpeningDirectMessagesTooFast           = 40003
	ErrCodeSendMessagesHasBeenTemporarilyDisabled = 40004
	ErrCodeRequestEntityTooLarge                  = 40005
	ErrCodeFeatureTemporarilyDisabledServerSide   = 40006
	ErrCodeUserIsBannedFromThisGuild              = 40007
	ErrCodeTargetIsNotConnectedToVoice            = 40032
	ErrCodeMessageAlreadyCrossposted              = 40033
	ErrCodeAnApplicationWithThatNameAlreadyExists = 40041
	ErrCodeInteractionHasAlreadyBeenAcknowledged  = 40060
	ErrCodeTagNamesMustBeUnique                   = 40061

	ErrCodeMissingAccess                                                = 50001
	ErrCodeInvalidAccountType                                           = 50002
	ErrCodeCannotExecuteActionOnDMChannel                               = 50003
	ErrCodeEmbedDisabled                                                = 50004
	ErrCodeGuildWidgetDisabled                                          = 50004
	ErrCodeCannotEditFromAnotherUser                                    = 50005
	ErrCodeCannotSendEmptyMessage                                       = 50006
	ErrCodeCannotSendMessagesToThisUser                                 = 50007
	ErrCodeCannotSendMessagesInVoiceChannel                             = 50008
	ErrCodeChannelVerificationLevelTooHigh                              = 50009
	ErrCodeOAuth2ApplicationDoesNotHaveBot                              = 50010
	ErrCodeOAuth2ApplicationLimitReached                                = 50011
	ErrCodeInvalidOAuthState                                            = 50012
	ErrCodeMissingPermissions                                           = 50013
	ErrCodeInvalidAuthenticationToken                                   = 50014
	ErrCodeTooFewOrTooManyMessagesToDelete                              = 50016
	ErrCodeCanOnlyPinMessageToOriginatingChannel                        = 50019
	ErrCodeInviteCodeWasEitherInvalidOrTaken                            = 50020
	ErrCodeCannotExecuteActionOnSystemMessage                           = 50021
	ErrCodeCannotExecuteActionOnThisChannelType                         = 50024
	ErrCodeInvalidOAuth2AccessTokenProvided                             = 50025
	ErrCodeMissingRequiredOAuth2Scope                                   = 50026
	ErrCodeInvalidWebhookTokenProvided                                  = 50027
	ErrCodeInvalidRole                                                  = 50028
	ErrCodeInvalidRecipients                                            = 50033
	ErrCodeMessageProvidedTooOldForBulkDelete                           = 50034
	ErrCodeInvalidFormBody                                              = 50035
	ErrCodeInviteAcceptedToGuildApplicationsBotNotIn                    = 50036
	ErrCodeInvalidAPIVersionProvided                                    = 50041
	ErrCodeFileUploadedExceedsTheMaximumSize                            = 50045
	ErrCodeInvalidFileUploaded                                          = 50046
	ErrCodeInvalidGuild                                                 = 50055
	ErrCodeInvalidMessageType                                           = 50068
	ErrCodeCannotDeleteAChannelRequiredForCommunityGuilds               = 50074
	ErrCodeInvalidStickerSent                                           = 50081
	ErrCodePerformedOperationOnArchivedThread                           = 50083
	ErrCodeBeforeValueIsEarlierThanThreadCreationDate                   = 50085
	ErrCodeCommunityServerChannelsMustBeTextChannels                    = 50086
	ErrCodeThisServerIsNotAvailableInYourLocation                       = 50095
	ErrCodeThisServerNeedsMonetizationEnabledInOrderToPerformThisAction = 50097
	ErrCodeThisServerNeedsMoreBoostsToPerformThisAction                 = 50101
	ErrCodeTheRequestBodyContainsInvalidJSON                            = 50109

	ErrCodeNoUsersWithDiscordTagExist = 80004

	ErrCodeReactionBlocked = 90001

	ErrCodeAPIResourceIsCurrentlyOverloaded = 130000

	ErrCodeTheStageIsAlreadyOpen = 150006

	ErrCodeCannotReplyWithoutPermissionToReadMessageHistory = 160002
	ErrCodeThreadAlreadyCreatedForThisMessage               = 160004
	ErrCodeThreadIsLocked                                   = 160005
	ErrCodeMaximumNumberOfActiveThreadsReached              = 160006
	ErrCodeMaximumNumberOfActiveAnnouncementThreadsReached  = 160007

	ErrCodeInvalidJSONForUploadedLottieFile                    = 170001
	ErrCodeUploadedLottiesCannotContainRasterizedImages        = 170002
	ErrCodeStickerMaximumFramerateExceeded                     = 170003
	ErrCodeStickerFrameCountExceedsMaximumOfOneThousandFrames  = 170004
	ErrCodeLottieAnimationMaximumDimensionsExceeded            = 170005
	ErrCodeStickerFrameRateOutOfRange                          = 170006
	ErrCodeStickerAnimationDurationExceedsMaximumOfFiveSeconds = 170007

	ErrCodeCannotUpdateAFinishedEvent             = 180000
	ErrCodeFailedToCreateStageNeededForStageEvent = 180002

	ErrCodeCannotEnableOnboardingRequirementsAreNotMet  = 350000
	ErrCodeCannotUpdateOnboardingWhileBelowRequirements = 350001
)

// An APIErrorMessage is an api error message returned from discord
type APIErrorMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
