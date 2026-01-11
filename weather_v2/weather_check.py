#!/usr/bin/env python3
"""
Simple weather checker using OpenWeatherMap API.
"""

import argparse
import json
import os
import socket
import sys
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import Any, Dict, Optional, Sequence

BASE_URL = "https://api.openweathermap.org/data/2.5/weather"
DEFAULT_TIMEOUT = 10
USER_AGENT = "weather-check/1.0"


@dataclass(frozen=True)
class WeatherInfo:
    city: str
    country: str
    temperature_c: float
    feels_like_c: float
    humidity: int
    description: str


def _read_json_response(response: Any) -> Dict[str, Any]:
    charset = response.headers.get_content_charset("utf-8")
    return json.loads(response.read().decode(charset))


def _parse_error_message(error: urllib.error.HTTPError) -> Optional[str]:
    try:
        charset = error.headers.get_content_charset("utf-8")
    except Exception:
        charset = "utf-8"
    try:
        body = error.read().decode(charset)
    except Exception:
        return None
    try:
        payload = json.loads(body)
    except json.JSONDecodeError:
        return None
    message = payload.get("message")
    return message if isinstance(message, str) else None


def get_weather(city: str, api_key: str, timeout: int = DEFAULT_TIMEOUT) -> Optional[Dict[str, Any]]:
    """
    Fetch weather data for a given city using OpenWeatherMap API.
    """
    params = urllib.parse.urlencode(
        {
            "q": city,
            "appid": api_key,
            "units": "metric",
        }
    )
    url = f"{BASE_URL}?{params}"
    request = urllib.request.Request(
        url,
        headers={
            "Accept": "application/json",
            "User-Agent": USER_AGENT,
        },
    )

    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            data = _read_json_response(response)
            cod = data.get("cod")
            if cod is not None and str(cod) != "200":
                message = data.get("message", "Unknown error")
                print(f"Error: API returned code {cod} - {message}", file=sys.stderr)
                return None
            return data
    except urllib.error.HTTPError as e:
        message = _parse_error_message(e)
        if e.code == 401:
            print("Error: Invalid API key", file=sys.stderr)
        elif e.code == 404:
            print(f"Error: City '{city}' not found", file=sys.stderr)
        elif message:
            print(f"Error: HTTP {e.code} - {message}", file=sys.stderr)
        else:
            print(f"Error: HTTP {e.code} - {e.reason}", file=sys.stderr)
        return None
    except urllib.error.URLError as e:
        reason = getattr(e, "reason", None)
        if isinstance(reason, socket.timeout):
            print("Error: Network request timed out", file=sys.stderr)
        else:
            print(f"Error: Network connection failed - {reason}", file=sys.stderr)
        return None
    except json.JSONDecodeError:
        print("Error: Invalid JSON response from API", file=sys.stderr)
        return None
    except Exception as e:
        print(f"Error: Unexpected error occurred - {str(e)}", file=sys.stderr)
        return None


def _parse_float(value: Any, label: str) -> Optional[float]:
    try:
        return float(value)
    except (TypeError, ValueError):
        print(f"Error: Missing or invalid {label} in response", file=sys.stderr)
        return None


def _parse_int(value: Any, label: str) -> Optional[int]:
    try:
        return int(value)
    except (TypeError, ValueError):
        print(f"Error: Missing or invalid {label} in response", file=sys.stderr)
        return None


def parse_weather(data: Dict[str, Any]) -> Optional[WeatherInfo]:
    if not isinstance(data, dict):
        print("Error: Unexpected response format", file=sys.stderr)
        return None

    city = data.get("name")
    if not isinstance(city, str) or not city:
        print("Error: Missing city name in response", file=sys.stderr)
        return None

    sys_info = data.get("sys")
    if not isinstance(sys_info, dict):
        print("Error: Missing system info in response", file=sys.stderr)
        return None
    country = sys_info.get("country")
    if not isinstance(country, str) or not country:
        print("Error: Missing country in response", file=sys.stderr)
        return None

    main_data = data.get("main")
    if not isinstance(main_data, dict):
        print("Error: Missing main weather data in response", file=sys.stderr)
        return None

    temp = _parse_float(main_data.get("temp"), "temperature")
    feels_like = _parse_float(main_data.get("feels_like"), "feels like temperature")
    humidity = _parse_int(main_data.get("humidity"), "humidity")

    weather_list = data.get("weather")
    if not isinstance(weather_list, list) or not weather_list:
        print("Error: Missing weather description in response", file=sys.stderr)
        return None
    first = weather_list[0]
    if not isinstance(first, dict):
        print("Error: Unexpected weather data format", file=sys.stderr)
        return None
    description = first.get("description")
    if not isinstance(description, str) or not description:
        print("Error: Missing weather description in response", file=sys.stderr)
        return None

    if temp is None or feels_like is None or humidity is None:
        return None

    return WeatherInfo(
        city=city,
        country=country,
        temperature_c=temp,
        feels_like_c=feels_like,
        humidity=humidity,
        description=description,
    )


def display_weather(info: WeatherInfo) -> None:
    print(f"\nWeather in {info.city}, {info.country}:")
    print(f"  Temperature: {info.temperature_c}°C (feels like {info.feels_like_c}°C)")
    print(f"  Conditions: {info.description.capitalize()}")
    print(f"  Humidity: {info.humidity}%")


def parse_args(argv: Optional[Sequence[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Check weather for a city.")
    parser.add_argument("city", help="City name to check weather for")
    parser.add_argument(
        "api_key",
        nargs="?",
        help="OpenWeatherMap API key (discouraged; use --api-key or OPENWEATHER_API_KEY)",
    )
    parser.add_argument(
        "-k",
        "--api-key",
        dest="api_key_override",
        help="OpenWeatherMap API key (overrides OPENWEATHER_API_KEY)",
    )
    parser.add_argument(
        "--timeout",
        type=int,
        default=DEFAULT_TIMEOUT,
        help="Request timeout in seconds (default: %(default)s)",
    )
    return parser.parse_args(argv)


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = parse_args(argv)

    city = args.city.strip()
    if not city:
        print("Error: City name cannot be empty", file=sys.stderr)
        return 1

    api_key = args.api_key_override or args.api_key or os.environ.get("OPENWEATHER_API_KEY")
    if api_key is not None:
        api_key = api_key.strip()

    if not api_key:
        print(
            "Error: API key not found. Set OPENWEATHER_API_KEY or use --api-key.",
            file=sys.stderr,
        )
        return 1

    if args.api_key and not args.api_key_override:
        print(
            "Warning: Passing the API key as a positional argument may expose it in shell history.",
            file=sys.stderr,
        )

    if args.timeout <= 0:
        print("Error: Timeout must be a positive number of seconds.", file=sys.stderr)
        return 1

    print(f"Checking weather for {city}...")
    weather_data = get_weather(city, api_key, timeout=args.timeout)
    if not weather_data:
        return 1

    info = parse_weather(weather_data)
    if not info:
        return 1

    display_weather(info)
    return 0


if __name__ == "__main__":
    sys.exit(main())
