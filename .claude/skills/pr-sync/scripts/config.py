"""Load pr-residents config and resolve per-org tokens.

Nothing user-specific is hardcoded: skills read identity/config by path. Tokens
live only in env (GITHUB_TOKEN_<ORG>); config holds at most the var-name prefix.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from typing import Any

import miniyaml


def _org_env_var(owner: str, prefix: str = "GITHUB_TOKEN") -> str:
    return f"{prefix}_{owner.upper().replace('-', '_')}"


@dataclass
class Config:
    repos: list[str]
    exclude_paths: list[str]
    escalation: dict[str, Any]
    token_prefix: str = "GITHUB_TOKEN"
    subscribed_repos: list[str] = field(default_factory=list)
    interests: list[str] = field(default_factory=list)  # path prefixes (cold-start relevance)

    def active_repos(self) -> list[str]:
        if self.subscribed_repos:
            return [r for r in self.repos if r in self.subscribed_repos]
        return self.repos

    def token_for(self, owner: str) -> str | None:
        return os.environ.get(_org_env_var(owner, self.token_prefix))

    def env_var_for(self, owner: str) -> str:
        return _org_env_var(owner, self.token_prefix)


def load(config_dir: str) -> Config:
    repos_cfg = miniyaml.load_file(os.path.join(config_dir, "repos.yml")) or {}
    escalation = miniyaml.load_file(os.path.join(config_dir, "escalation.yml")) or {}

    token_prefix = "GITHUB_TOKEN"
    subscribed: list[str] = []
    interests: list[str] = []
    user_path = os.path.join(config_dir, "user.yml")
    if os.path.exists(user_path):
        user_cfg = miniyaml.load_file(user_path) or {}
        env = user_cfg.get("env") or {}
        token_prefix = env.get("github_token_prefix") or token_prefix
        subscribed = user_cfg.get("subscribed_repos") or []
        interests = user_cfg.get("interests") or []

    return Config(
        repos=repos_cfg.get("repos") or [],
        exclude_paths=repos_cfg.get("exclude_paths") or [],
        escalation=escalation,
        token_prefix=token_prefix,
        subscribed_repos=subscribed,
        interests=interests,
    )
