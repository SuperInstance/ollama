from setuptools import setup, find_packages

setup(
    name="ollama-intelligence-monitor",
    version="0.1.0",
    description="SuperInstance Intelligence Monitor for Ollama — detect model redundancy, optimize GPU usage",
    author="SuperInstance",
    packages=find_packages(),
    package_dir={"": "."},
    python_requires=">=3.10",
    entry_points={
        "console_scripts": [
            "ollama-intel=intelligence_monitor.cli:main",
        ],
    },
)
