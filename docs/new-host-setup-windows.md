# Windows Server Host Setup
1. On your usual client machine (different from your intended server machine), navigate to `C:\Program Files\Oculus\Software\Software\ready-at-dawn-echo-arena\_local\config.json` (for PC) OR `/sdcard/readyatdawn/config.json` (for Quest). Replace the contents of the config file with the following, and update it with your own information:
```
{
    "apiservice_host":  "http://g.echovrce.com:80/api",
    "configservice_host":  "ws://g.echovrce.com:80/config",
    "loginservice_host":  "ws://g.echovrce.com:80/login?discordid=YOURDISCORDID&password=YOURPASSWORD",
    "matchingservice_host":  "ws://g.echovrce.com:80/matching",
    "serverdb_host":  "ws://g.echovrce.com:80/serverdb?discordid=YOURDISCORDID&password=YOURPASSWORD",
    "transactionservice_host":  "ws://g.echovrce.com:80/transaction",
    "publisher_lock":  "echovrce"
}
```

> [!WARNING]
> The password used in your config file should not be any password you typically use.

2. Make sure your server machine has a static IP on your local network. (e.g., 192.168.1.100)
3. Forward port 6792 TCP/UDP to your server machine. For each additional gameserver you want to run, increase the port forward range by 1, counting up from 6792. (e.g., for 3 servers, forward 6792-6794)
4. Install Echo on your server machine via [Mia's Installer](https://github.com/BL00DY-C0D3/Echo-VR-Installer/releases). Download and extract `newhostfiles.zip` onto your server machine.
5. Copy the new `config.json` file you used to log in on your regular client machine to the PC config location on your server machine.

> [!NOTE]
> If you were given a specific region ID to use, add `&regions=REGIONID` after your password on the `serverdb_host` line of the config file. Otherwise, you can leave it out. The region ID only needs to go in the server's config file.

7. Extract the contents of `gunpatch.zip` to `\...\ready_at_dawn_echo_arena\` and run `patch.bat`.
8. Put `dbgcore.dll` and `pnsradgameserver.dll` in the `\...\ready_at_dawn_echo_arena\bin\win10\` directory. Replace any duplicate files.
9. Download the server monitor to auto-restart in the event of a crash [here](https://github.com/marshmallow-mia/Echo-VR-Server-Error-Monitoring). Edit the file and customize it before running. 

> [!TIP]
> Ensure you've installed PowerShell 7, and allowed .ps1 execution on your machine by running `Set-ExecutionPolicy Bypass` in an admin terminal.

9. __If you are running more than 3 servers__, go to `...\ready-at-dawn-echo-arena\sourcedb\rad15\json\r14\config\netconfig_dedicatedserver.json` and update `retries` to the number of servers you are running plus 1.

### Optional, but highly recommended:
- Create a .bat file in the same directory that contains the following line: `pwsh Echo-VR-Server-Error-Monitoring.ps1`. Create a shortcut to the .bat file and place that in the windows startup folder.
- Ensure your router is not blocking P2P traffic to the server. This is especially necessary for hosts with UniFi hardware.
