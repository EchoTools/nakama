package evr

import (
	"encoding/binary"
	"fmt"
)

// A ConfigRequestv2 response message to the client, indicating failure to retrieve the requested resource.
type ConfigSuccess struct {
	Type     Symbol
	Id       Symbol
	Resource any
}

func (m ConfigSuccess) String() string {
	return fmt.Sprintf("%T(type=%v, id=%v)", m, m.Type, m.Id)
}

func NewConfigSuccess(_type string, id string, resource any) *ConfigSuccess {
	return &ConfigSuccess{
		Type:     ToSymbol(_type),
		Id:       ToSymbol(id),
		Resource: resource,
	}
}

func (m *ConfigSuccess) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Type) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Id) },
		func() error { return s.StreamJson(&m.Resource, true, ZstdCompression) },
	})
}

func GetDefaultConfigResource(_type string, id string) string {
	switch _type + ":" + id {
	case "main_menu:main_menu":
		return DefaultMainMenuConfigResource
	case "active_battle_pass_season:active_battle_pass_season":
		return DefaultActiveBattlePassSeasonConfigResource
	case "active_store_entry:active_store_entry":
		return DefaultActiveStoreEntryConfigResource
	case "active_store_featured_entry:active_store_featured_entry":
		return DefaultActiveStoreFeaturedEntryConfigResource
	default:
		return ""
	}
}

const (
	DefaultMainMenuConfigResource string = `{
		"type": "main_menu",
		"id": "main_menu",
		"_ts": 0,
		"news": {
			"offseason": {
			"texture": "None",
			"link": "https://en.wikipedia.org/wiki/Lone_Echo"
			},
			"sentiment": {
			"texture": "ui_mnu_news_latest",
			"link": "https://en.wikipedia.org/wiki/Lone_Echo"
			}
		},
		"splash": {
			"offseason": {
			"texture": "ui_menu_splash_screen_poster_a_shutdown_clr",
			"link": "https://en.wikipedia.org/wiki/Lone_Echo"
			}
		},
		"splash_version": 1,
		"help_link": "https://en.wikipedia.org/wiki/Lone_Echo",
		"news_link": "https://en.wikipedia.org/wiki/Lone_Echo",
		"discord_link": "https://en.wikipedia.org/wiki/Lone_Echo"
		}`
	DefaultActiveBattlePassSeasonConfigResource string = `{
		"learn_more_link": null,
		"archives": {
			"marketing_menu": null,
			"marketing_lobby": null
		},
		"id": "active_battle_pass_season",
		"offseason": false,
		"days_remaining_to_trigger_upsell": 0,
		"ui": {
			"offseason_start_date_text": "INVALID",
			"offseason_tier_unlocked_text": "INVALID",
			"season_progression_upsell_text": "INVALID",
			"season_track_header_text_upsell": "INVALID",
			"icon_texture_texture": null,
			"offseason_time_to_next_season_text": "INVALID",
			"season_track_header_text_start": "INVALID"
		},
		"premium_price": {
			"echopoints": 0
		},
		"bp_xp_boost_reward_solo": 0,
		"bp_currency_rewards": null,
		"premium_skus": null,
		"tier_rewards": null,
		"marketing": {
			"menu_season_panel_upsell_texture": null,
			"lobby_poster_upsell_texture": "ui_mnu_news_latest",
			"lobby_poster_start_texture": "ui_mnu_news_latest",
			"menu_season_panel_start_texture": null
		},
		"bp_xp_results": {
			"win": 0,
			"loss": 0
		},
		"bp_xp_requirements": {
			"use_constant_tier_xp_requirements": true,
			"constant_tier_xp_requirements": 0,
			"variable_tier_xp_requirements": null
		},
		"season_number": 3246,
		"tier_price": {
			"echopoints": 0
		},
		"menu_link_upsell": null,
		"_ts": 0,
		"description": null,
		"menu_link": null,
		"bp_xp_boost_reward_party": 0,
		"title": "Echo Pass",
		"type": "active_battle_pass_season"
	}`

	DefaultActiveStoreEntryConfigResource string = `{
		"entry_number": 0,
		"marketing": {
			"menu_store_panel_start_texture": "ui_menu_splash_screen_poster_a_shutdown_clr",
			"menu_store_panel_upsell_texture": "ui_menu_splash_screen_poster_a_shutdown_clr",
			"lobby_poster_upsell_texture": "ui_menu_splash_screen_poster_a_shutdown_clr",
			"lobby_poster_start_texture": "ui_menu_splash_screen_poster_a_shutdown_clr"
		},
		"ui": {
			"store_panel_header_start_text": "ITEM INFO",
			"store_panel_header_upsell_text": "ITEM INFO | HERE FOREVER!"
		},
		"type": "active_store_entry",
		"store_slots": [
			{
				"reward_items": [
					{
						"bundle": {
							"sku": "store_rwd_chassis_body_s11_a_bundled",
							"price": {
								"echopoints": 0
							}
						},
						"reward_id": "rwd_chassis_body_s11_a"
					},
					{
						"bundle": {
							"sku": "store_rwd_booster_default_bundled",
							"price": {
								"echopoints": 0
							}
						},
						"reward_id": "rwd_booster_default"
					}
				],
				"bundle_icon": null,
				"bundle_name": "SERIES 11",
				"bundle_allow_individual_item_purchases": false,
				"bundle_description": null
			},
			{
				"reward_items": [
					{
						"bundle": {
							"sku": "store_rwd_chassis_s11_flame_a_bundled",
							"price": {
								"echopoints": 0
							}
						},
						"reward_id": "rwd_chassis_s11_flame_a"
					},
					{
						"bundle": {
							"sku": "store_rwd_booster_s11_s1_a_fire_bundled",
							"price": {
								"echopoints": 0
							}
						},
						"reward_id": "rwd_booster_s11_s1_a_fire"
					}
				],
				"bundle_icon": null,
				"bundle_name": "SERIES 11 FLAMEY",
				"bundle_allow_individual_item_purchases": false,
				"bundle_description": null
			},
			{
				"reward_items": [
					{
						"bundle": {
							"sku": "store_rwd_chassis_s11_retro_a_bundled",
							"price": {
								"echopoints": 0
							}
						},
						"reward_id": "rwd_chassis_s11_retro_a"
					},
					{
						"bundle": {
							"sku": "store_rwd_booster_s11_s1_a_retro_bundled",
							"price": {
								"echopoints": 0
							}
						},
						"reward_id": "rwd_booster_s11_s1_a_retro"
					}
				],
				"bundle_icon": null,
				"bundle_name": "SERIES 11 RETRO",
				"bundle_allow_individual_item_purchases": false,
				"bundle_description": null
			}
		],
		"_ts": 0,
		"days_remaining_to_trigger_upsell": 0,
		"id": "active_store_entry"
	}`
	DefaultActiveStoreFeaturedEntryConfigResource string = `{
		"entry_number": 0,
		"marketing": {
			"menu_store_panel_start_texture": "ui_menu_splash_screen_poster_a_shutdown_clr",
			"menu_store_panel_upsell_texture": "ui_menu_splash_screen_poster_a_shutdown_clr",
			"lobby_poster_upsell_texture": "ui_menu_splash_screen_poster_a_shutdown_clr",
			"lobby_poster_start_texture": "ui_menu_splash_screen_poster_a_shutdown_clr"
		},
		"ui": {
			"store_panel_header_start_text": "SEASON 3246",
			"store_panel_header_upsell_text": "FEATURED ITEM INFO | HERE FOREVER!"
		},
		"type": "active_store_featured_entry",
		"store_slots": [
			{
				"reward_items": [
					{
						"bundle": {
							"sku": "store_rwd_chassis_ranger_a_bundled",
							"price": {
								"echopoints": 999
							}
						},
						"reward_id": "rwd_chassis_ranger_a"
					},
					{
						"bundle": {
							"sku": "store_rwd_bracer_ranger_a_bundled",
							"price": {
								"echopoints": 999
							}
						},
						"reward_id": "rwd_bracer_ranger_a"
					},
					{
						"bundle": {
							"sku": "store_rwd_booster_ranger_a_bundled",
							"price": {
								"echopoints": 999
							}
						},
						"reward_id": "rwd_booster_ranger_a"
					}
				],
				"bundle_icon": "ui_menu_splash_screen_poster_a_shutdown_clr",
				"bundle_name": "SEASON 3246",
				"bundle_allow_individual_item_purchases": false,
				"bundle_description": null
			}
		],
		"_ts": 0,
		"days_remaining_to_trigger_upsell": 0,
		"id": "active_store_featured_entry"
	}`
)
