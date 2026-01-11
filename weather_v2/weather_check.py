#!/usr/bin/env python3
"""
Weather checker using OpenWeatherMap API.
Retrieves current weather information for a specified city.
"""

import os
import sys
import json
from urllib.request import urlopen, Request
from urllib.error import URLError, HTTPError
from urllib.parse import urlencode
from typing import Optional, Dict, Any


class WeatherAPIError(Exception):
    """Custom exception for weather API errors."""
    pass


def get_weather(city: str, api_key: Optional[str] = None) -> Dict[str, Any]:
    """
    Fetch weather data for a given city using OpenWeatherMap API.

    Args:
        city (str): Name of the city to check weather for
        api_key (str, optional): OpenWeatherMap API key. If not provided,
                                will attempt to read from OPENWEATHER_API_KEY env var

    Returns:
        dict: Weather data containing temperature, description, humidity, etc.

    Raises:
        WeatherAPIError: If API request fails or returns an error
        ValueError: If city name is empty or API key is missing
    """
    if not city or not city.strip():
        raise ValueError("City name cannot be empty")

    if api_key is None:
        api_key = os.environ.get('OPENWEATHER_API_KEY')

    if not api_key:
        raise ValueError(
            "API key is required. Set OPENWEATHER_API_KEY environment variable "
            "or pass api_key parameter"
        )

    base_url = "https://api.openweathermap.org/data/2.5/weather"
    params = {
        'q': city.strip(),
        'appid': api_key,
        'units': 'metric'
    }

    url = f"{base_url}?{urlencode(params)}"

    try:
        request = Request(url)
        request.add_header('User-Agent', 'WeatherChecker/1.0')

        with urlopen(request, timeout=10) as response:
            try:
                payload = response.read().decode('utf-8')
            except UnicodeDecodeError as exc:
                raise WeatherAPIError("Failed to decode API response") from exc

            data = json.loads(payload)
            if not isinstance(data, dict):
                raise WeatherAPIError("Unexpected API response format")

            # Check for API error codes in 200 responses
            # OpenWeatherMap sometimes returns 200 with an error 'cod' in body
            cod = data.get('cod')
            if cod is not None and str(cod) != '200':
                message = data.get('message') or 'Unknown error'
                raise WeatherAPIError(f"API Error {cod}: {message}")

            return data

    except HTTPError as e:
        if e.code == 404:
            raise WeatherAPIError(f"City '{city}' not found")
        elif e.code == 401:
            raise WeatherAPIError("Invalid API key")
        elif e.code == 429:
            raise WeatherAPIError("Rate limit exceeded (HTTP 429)")
        else:
            raise WeatherAPIError(f"HTTP error {e.code}: {e.reason}")

    except URLError as e:
        raise WeatherAPIError(f"Network error: {e.reason}")

    except json.JSONDecodeError:
        raise WeatherAPIError("Failed to parse API response")

    except Exception as e:
        # Re-raise WeatherAPIError directly
        if isinstance(e, WeatherAPIError):
            raise e
        raise WeatherAPIError(f"Unexpected error: {str(e)}")


def format_weather_output(weather_data: Dict[str, Any]) -> str:
    """
    Format weather data into a human-readable string.

    Args:
        weather_data (dict): Raw weather data from API

    Returns:
        str: Formatted weather information
    """
    city_name = weather_data.get('name') or 'Unknown'
    if not isinstance(city_name, str):
        city_name = str(city_name)

    sys_data = weather_data.get('sys')
    if not isinstance(sys_data, dict):
        sys_data = {}
    country = sys_data.get('country', '')
    if country and not isinstance(country, str):
        country = str(country)

    main_data = weather_data.get('main')
    if not isinstance(main_data, dict):
        main_data = {}
    temp = main_data.get('temp')
    feels_like = main_data.get('feels_like')
    humidity = main_data.get('humidity')

    # Safe access for weather description
    weather_list = weather_data.get('weather')
    weather_desc = 'N/A'
    if isinstance(weather_list, list) and weather_list:
        first_weather = weather_list[0]
        if isinstance(first_weather, dict):
            desc = first_weather.get('description')
            if desc:
                weather_desc = str(desc).capitalize()

    wind_data = weather_data.get('wind')
    if not isinstance(wind_data, dict):
        wind_data = {}
    wind_speed = wind_data.get('speed')

    location = f"{city_name}, {country}" if country else city_name

    # Helper to format values that might be None
    def fmt(val: Any, unit: str = "") -> str:
        return f"{val}{unit}" if val is not None else "N/A"

    output = [
        f"Weather for {location}:",
        f"  Condition: {weather_desc}",
        f"  Temperature: {fmt(temp, '°C')} (feels like {fmt(feels_like, '°C')})",
        f"  Humidity: {fmt(humidity, '%')}",
        f"  Wind Speed: {fmt(wind_speed, ' m/s')}"
    ]

    return '\n'.join(output)


def main() -> int:
    """Main function to run the weather checker."""
    if len(sys.argv) < 2:
        print("Usage: python weather_check.py <city_name>")
        print("Example: python weather_check.py London")
        print("\nNote: Set OPENWEATHER_API_KEY environment variable with your API key")
        sys.exit(1)

    city = ' '.join(sys.argv[1:])

    try:
        weather_data = get_weather(city)
        output = format_weather_output(weather_data)
        print(output)
        return 0

    except ValueError as e:
        print(f"Error: {e}", file=sys.stderr)
        return 1

    except WeatherAPIError as e:
        print(f"Weather API Error: {e}", file=sys.stderr)
        return 1

    except KeyboardInterrupt:
        print("\nOperation cancelled by user", file=sys.stderr)
        return 130

    except Exception as e:
        print(f"Unexpected error: {e}", file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
