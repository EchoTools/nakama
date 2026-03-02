#!/usr/bin/env python3
"""
Nakama Authenticated Curl - Like curl -s but with Nakama authentication.

This script authenticates to Nakama using credentials from environment variables
or .env file, then fetches a URL with the authentication token as a bearer token.
"""

import argparse
import os
import sys
import time
from typing import Optional

import requests
from dotenv import load_dotenv


class NakamaAuthenticator:
    """Handles Nakama authentication and token management."""

    def __init__(
        self,
        nakama_url: str,
        username: str,
        password: str,
        http_key: str,
        server_key: str,
    ):
        self.nakama_url = nakama_url
        self.username = username
        self.password = password
        self.http_key = http_key
        self.server_key = server_key
        self._token: Optional[str] = None
        self._token_expiry: Optional[float] = None

    def authenticate(self) -> Optional[str]:
        """Authenticate to Nakama and get a session token."""
        if not self.username or not self.password or not self.http_key:
            print("[!] Missing required credentials", file=sys.stderr)
            return None

        try:
            auth_url = f"{self.nakama_url}/v2/rpc/account/authenticate/password?unwrap&http_key={self.http_key}"
            payload = {
                "username": self.username,
                "password": self.password,
                "create": True,
            }
            headers = {
                "Authorization": f"Bearer {self.server_key}",
                "Content-Type": "application/json",
            }

            resp = requests.post(auth_url, json=payload, headers=headers, timeout=10)
            resp.raise_for_status()
            data = resp.json()

            if "token" in data:
                self._token = data["token"]
                # Token expires in 60 minutes, we refresh at 50 minutes
                self._token_expiry = time.time() + (50 * 60)
                return self._token
            else:
                print("[!] No token in authentication response", file=sys.stderr)
                return None

        except requests.exceptions.RequestException as e:
            print(f"[!] Nakama authentication failed: {e}", file=sys.stderr)
            return None

    def get_token(self) -> Optional[str]:
        """Get current token, refreshing if necessary."""
        if self._token and self._token_expiry and time.time() < self._token_expiry:
            return self._token

        return self.authenticate()


def fetch_url(url: str, token: str) -> None:
    """Fetch URL with bearer token authentication and print response."""
    try:
        headers = {
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
        }

        resp = requests.get(url, headers=headers, timeout=30)
        resp.raise_for_status()

        # Print response body (like curl -s)
        print(resp.text)

    except requests.exceptions.RequestException as e:
        print(f"[!] Request failed: {e}", file=sys.stderr)
        sys.exit(1)


def main():
    """Main entry point."""
    # Load environment variables from .env file
    load_dotenv()

    parser = argparse.ArgumentParser(
        description="Fetch URL with Nakama authentication (like curl -s with auth)",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  %(prog)s https://g.echovrce.com/v2/rpc/match/list
  %(prog)s --nakama-url https://custom.server.com:7350 https://example.com/api/endpoint
  
Environment variables (or use .env file):
  NAKAMA_URL              Nakama server URL (default: https://g.echovrce.com:7350)
  NAKAMA_USERNAME         Username for authentication
  NAKAMA_PASSWORD         Password for authentication
  NAKAMA_HTTP_KEY         HTTP key for Nakama API
  NAKAMA_SERVER_KEY       Server key for Nakama API
        """,
    )

    parser.add_argument(
        "url",
        help="URL to fetch (with bearer token authentication)",
    )
    parser.add_argument(
        "--nakama-url",
        default=os.getenv("NAKAMA_URL", "https://g.echovrce.com:7350"),
        help="Nakama server URL (default: NAKAMA_URL env or https://g.echovrce.com:7350)",
    )
    parser.add_argument(
        "--username",
        default=os.getenv("NAKAMA_USERNAME", ""),
        help="Username for authentication (default: NAKAMA_USERNAME env)",
    )
    parser.add_argument(
        "--password",
        default=os.getenv("NAKAMA_PASSWORD", ""),
        help="Password for authentication (default: NAKAMA_PASSWORD env)",
    )
    parser.add_argument(
        "--http-key",
        default=os.getenv("NAKAMA_HTTP_KEY", ""),
        help="HTTP key for Nakama API (default: NAKAMA_HTTP_KEY env)",
    )
    parser.add_argument(
        "--server-key",
        default=os.getenv("NAKAMA_SERVER_KEY", ""),
        help="Server key for Nakama API (default: NAKAMA_SERVER_KEY env)",
    )
    parser.add_argument(
        "-v",
        "--verbose",
        action="store_true",
        help="Enable verbose output (authentication progress)",
    )

    args = parser.parse_args()

    # Validate required credentials
    if not args.username or not args.password or not args.http_key:
        print(
            "[!] Error: Missing required credentials. Set via environment variables or command line.",
            file=sys.stderr,
        )
        print(
            "[!] Required: NAKAMA_USERNAME, NAKAMA_PASSWORD, NAKAMA_HTTP_KEY",
            file=sys.stderr,
        )
        sys.exit(1)

    # Create authenticator
    authenticator = NakamaAuthenticator(
        nakama_url=args.nakama_url,
        username=args.username,
        password=args.password,
        http_key=args.http_key,
        server_key=args.server_key,
    )

    # Authenticate
    if args.verbose:
        print(f"[*] Authenticating to {args.nakama_url}...", file=sys.stderr)

    token = authenticator.get_token()
    if not token:
        print("[!] Authentication failed", file=sys.stderr)
        sys.exit(1)

    if args.verbose:
        print("[*] Authentication successful", file=sys.stderr)
        print(f"[*] Fetching: {args.url}", file=sys.stderr)

    # Fetch URL with token
    fetch_url(args.url, token)


if __name__ == "__main__":
    main()
