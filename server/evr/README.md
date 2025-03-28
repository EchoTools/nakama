# Protocol

## Main Menu

This sequence gets to the main menu

```graphviz

GameClient->ConfigService:**ConfigRequestv2**\n--config_info=//main_menu--//
GameClient<-ConfigService:**ConfigSuccess**\n--type=//"main_menu"//\nid=//"main_menu"//\npayload=//<666 bytes>--//
GameClient<-ConfigService:**STcpConnectionUnrequireEvent**
GameClient->LoginService:**LogInRequestv2**\n--session=//00000000-0000-0000-0000-000000000000//\nuser_id=//OVR_ORG-123412341234//\nlogin_data=//{}//--
GameClient<-LoginService:**LogInSuccess**\n--session=//2a4a1b12-2bf9-11ef-8dd5-66d3ff8a653b//\nuser_id=//OVR_ORG-123412341234--//
GameClient<-LoginService:**LoginSettings**{...}
GameClient<-LoginService:**STcpConnectionUnrequireEvent**
GameClient->ConfigService:**ConfigRequestv2**\n--config_info=//active_battle_pass_season--//
GameClient<-ConfigService:**ConfigSuccess**\n--type=//"active_battle_pass_season"//\nid=//"active_battle_pass_season"//\npayload=//<1390 bytes>--//
GameClient<-ConfigService:**STcpConnectionUnrequireEvent**
GameClient->LoginService:**LoggedInUserProfileRequest**\n--session=//2a4a1b12-2bf9-11ef-8dd5-66d3ff8a653b//\nuser_id=//{OVR_ORG 123412341234}//\nprofile_request={}--//
GameClient<-LoginService:**LoggedInUserProfileSuccess**\n--user_id=//{OVR_ORG 123412341234}--//
GameClient->LoginService:**DocumentRequestv2**\n--lang=//en//\nt=//eula--//
GameClient<-LoginService:**DocumentSuccess**\n--type=//evr.EULADocument--//
GameClient<-LoginService:**STcpConnectionUnrequireEvent**
GameClient->ConfigService:**ConfigRequestv2**\n--config_info=//active_store_entry--//
GameClient<-ConfigService:**ConfigSuccess**\n--type=//"active_store_entry"//\nid=//"active_store_entry"//\npayload=//<2634 bytes>--//
GameClient<-ConfigService:**STcpConnectionUnrequireEvent**
GameClient->ConfigService:**ConfigRequestv2**\n--config_info=//active_store_featured_entry--//
GameClient<-ConfigService:**ConfigSuccess**\n--type=//"active_store_featured_entry"//\nid=//"active_store_featured_entry"//\npayload=//<1595 bytes>--//
GameClient<-ConfigService:**STcpConnectionUnrequireEvent**
GameClient->LoginService:**ChannelInfoRequest**\n
GameClient<-LoginService:**ChannelInfoResponse**\n--EchoVRCE...--
GameClient<-LoginService:**STcpConnectionUnrequireEvent**
GameClient->LoginService:**UpdateProfile**\n--session=//2a4a1b12-2bf9-11ef-8dd5-66d3ff8a653b//\nevr_id=//OVR_ORG-123412341234--//
GameClient<-LoginService:**UpdateProfileSuccess**\n--user_id=//OVR_ORG-123412341234--//
GameClient<-LoginService:**STcpConnectionUnrequireEvent**
```