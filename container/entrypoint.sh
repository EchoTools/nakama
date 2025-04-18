#!/bin/bash
cd /nakama-app

# Replace Startup Variables
MODIFIED_STARTUP=`${STARTUP}`
echo ":/nakama-app$ ${MODIFIED_STARTUP}"

# Run the Server
${MODIFIED_STARTUP}
