#!/usr/bin/env python3
"""
Simple weather checker using OpenWeatherMap API.
"""

import json
import os
import sys
from urllib.request import urlopen, Request
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode


def check_weather(city, api_key):
    """
    Check weather for a given city using OpenWeatherMap API.

    Args:
        city: Name of the city to check weather for
        api_key: OpenWeatherMap API key

    Returns:
        dict: Weather data including temperature, description, etc.

    Raises:
        ValueError: If city or api_key is empty
        HTTPError: If API request fails
        URLError: If network connection fails
    """
    if not city or not city.strip():
        raise ValueError("City name cannot be empty")

    if not api_key or not api_key.strip():
        raise ValueError("API key cannot be empty")

    # Build API URL (HTTPS)
    base_url = "https://api.openweathermap.org/data/2.5/weather"
    params = {
        'q': city.strip(),
        'appid': api_key.strip(),
        'units': 'metric'
    }
    url = f"{base_url}?{urlencode(params)}"

    try:
        # Make request with proper headers
        request = Request(
            url,
            headers={
                'User-Agent': 'Mozilla/5.0',
                'Accept': 'application/json',
            },
        )
        with urlopen(request, timeout=10) as response:
            encoding = response.headers.get_content_charset('utf-8')
            raw_body = response.read()

        try:
            data = json.loads(raw_body.decode(encoding))
        except (UnicodeDecodeError, json.JSONDecodeError) as e:
            raise ValueError(f"Invalid JSON response: {e}") from e

        if not isinstance(data, dict):
            raise ValueError("Invalid response from API: expected JSON object")

        api_code = data.get('cod')
        if api_code not in (None, 200, "200"):
            message = data.get('message', 'Unknown error')
            raise ValueError(f"API error ({api_code}): {message}")

        # Extract relevant weather information safely
        weather_list = data.get('weather')
        if not isinstance(weather_list, list):
            weather_list = []

        description = "Unknown"
        if weather_list:
            first = weather_list[0]
            if isinstance(first, dict):
                description = first.get('description') or "Unknown"

        main_data = data.get('main')
        if not isinstance(main_data, dict):
            main_data = {}

        wind_data = data.get('wind')
        if not isinstance(wind_data, dict):
            wind_data = {}

        sys_data = data.get('sys')
        if not isinstance(sys_data, dict):
            sys_data = {}

        weather_info = {
            'city': data.get('name') or 'Unknown',
            'country': sys_data.get('country') or 'Unknown',
            'temperature': main_data.get('temp'),
            'feels_like': main_data.get('feels_like'),
            'humidity': main_data.get('humidity'),
            'description': description,
            'wind_speed': wind_data.get('speed')
        }

        return weather_info

    except HTTPError as e:
        if e.code == 401:
            raise ValueError("Invalid API key") from e
        elif e.code == 404:
            raise ValueError(f"City '{city}' not found") from e
        elif e.code == 429:
            retry_after = e.headers.get("Retry-After") if e.headers else None
            if retry_after:
                raise ValueError(f"Rate limit exceeded. Retry after {retry_after} seconds.") from e
            raise ValueError("Rate limit exceeded. Try again later.") from e
        else:
            raise

    except URLError as e:
        raise URLError(f"Network error: {e.reason}") from e


def format_value(value, unit=""):
    """Format a value with units or return N/A when missing."""
    if value is None:
        return "N/A"
    return f"{value}{unit}"


def format_weather(weather_info):
    """Format weather information for display."""
    temperature = format_value(weather_info['temperature'], "°C")
    feels_like = format_value(weather_info['feels_like'], "°C")
    humidity = format_value(weather_info['humidity'], "%")
    wind_speed = format_value(weather_info['wind_speed'], " m/s")

    return f"""
Weather for {weather_info['city']}, {weather_info['country']}:
  Temperature: {temperature} (feels like {feels_like})
  Condition: {weather_info['description'].title()}
  Humidity: {humidity}
  Wind Speed: {wind_speed}
"""


def main():
    """Main function to run the weather checker."""
    # Get API key from environment variable
    api_key = os.getenv('OPENWEATHER_API_KEY')

    if not api_key:
        print("Error: OPENWEATHER_API_KEY environment variable not set", file=sys.stderr)
        print("Usage: export OPENWEATHER_API_KEY='your_api_key'", file=sys.stderr)
        sys.exit(1)

    # Get city from command line argument
    if len(sys.argv) < 2:
        print("Usage: python weather_check.py <city_name>", file=sys.stderr)
        print("Example: python weather_check.py London", file=sys.stderr)
        sys.exit(1)

    city = " ".join(sys.argv[1:]).strip()

    try:
        weather_info = check_weather(city, api_key)
        print(format_weather(weather_info))

    except ValueError as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)

    except HTTPError as e:
        print(f"HTTP Error: {e.code} {e.reason}", file=sys.stderr)
        sys.exit(1)

    except URLError as e:
        print(f"Network Error: {e}", file=sys.stderr)
        sys.exit(1)

    except Exception as e:
        print(f"Unexpected error: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
