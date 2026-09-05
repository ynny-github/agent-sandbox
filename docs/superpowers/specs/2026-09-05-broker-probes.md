# Probe profiles behind the 2026-09-05 broker design

nono 0.74.0 carrying the NixOS ELF patch, Linux 6.18.42, NixOS, measured 2026-09-05.
Each profile was run as `nono run --silent --profile <file> --workdir <dir> -- <argv>`.

The `b*` and `sh*` probes use a stand-in broker: a small Go program that resolves
its argument on PATH and execs it (`b*`), or that parses a shell line with
`mvdan.cc/sh` and routes every simple command through `interp.ExecHandler` (`sh*`).

## Index

| profile | tested | result |
| --- | --- | --- |
| `p` | $WORKDIR expansion in filesystem and inside command_policies | expands in both |
| `p2` | bash entrypoint, ls deny with executable mis-pinned to the coreutils multi-call binary | deny did not fire; the probe was wrong, not a finding |
| `p3` | can_use listing a callee whose from.<caller> is deny | profile parse error: contradictory_chained_allow |
| `p4` | invocation_policy default:deny on the same mis-pinned edge | did not fire; same bad probe |
| `p5` | bash entrypoint, git deny rules with no default key | all git denied - default is deny when the block is present |
| `p6` | same, default:allow, no exec_paths on the bash child | nothing runs; bash cannot reach the shims through PATH |
| `p7` | bash entrypoint, broad exec_paths, git denials | PATH form denied with the reason; ABSOLUTE path bypasses |
| `p8` | git as the session entrypoint, no bash anywhere | denial enforced; absolute path refused as direct exec bypass denied |
| `p9` | bash child, exec_paths narrowed to git's real binary | both PATH and absolute form bypass |
| `p10` | p7 plus command_policies.deny_direct_exec_bypass = [git] | disables the shim; PATH form bypasses too |
| `p11` | bash child, exec_paths = ['/tmp'] (the shim directory) | bypass blocked, but shims are not executable either; nothing runs |
| `p12` | p11 plus /tmp on the child's fs_read | still nothing runs |
| `base` | no command_policies, session network = developer | example.com 000, pypi 200, github 200 - session-level domain filtering works |
| `base2` | no command_policies, no network section | pypi 200, github 200 |
| `n1` | bash child with no network key, no session network section | pypi 000 - the floor is network-off |
| `n2` | bash child with no network key, session network = developer | pypi 000 - session network does not descend |
| `n3` | curl child network.allow_domain = [pypi] | pypi 000 |
| `n5` | curl child network.allow_all, session = developer | pypi 000 |
| `n8` | bash child (from.session) network.allow_all | pypi 200 - a child can have network |
| `n9` | n5 plus an invocation_policy deny on curl | deny fired - the shim was in the path, so 000 was not a shim miss |
| `n10` | bash child allow_all and curl child allow_all | pypi 200 |
| `n12` | bash child allow_all, curl child allow_domain = [pypi] only | pypi 200 AND github 200 - a child's allow_domain is ignored |
| `s1` | curl as the session entrypoint with network.allow_all, session = developer | pypi 200, example.com 000 - per-command on/off under a session ceiling |
| `s2` | same with the network key removed | pypi 000 |
| `b1` | broker stand-in as a policy command, NO exec_paths | reaches its can_use shims; absolute path, symlink, non-policy commands and bash all refused at execve |
| `b2` | same with exec_paths = [git's real binary] | absolute path bypasses - exec_paths is precisely the hole |
| `c1` | broker with no network, curl child allow_all | pypi 200 - a child is configured independently of its caller |
| `c3` | broker allow_all, curl child with no network key | pypi 000 |
| `sh1` | broker interpreting the line with mvdan.cc/sh; git and rg both policy commands | shell language works; git deny enforced; policy-to-policy pipeline hangs |
| `sh2` | same, but rg moved to the broker's exec_paths instead of being a policy command | pipeline works, git deny still unbypassable, bash still refused |
| `dp1` | `cmd/doctor.go`'s `checkToolSandbox` probe profile, exactly as committed, run against a working nono, a separately patched nono, and a build with the ELF-closure bug still present | OK on both working builds; the broken build fails immediately with nono's own ELF-resolution error, before the probe command ever runs |

## Profiles

Reproduced in full for the probes the design rests on. The rest differ from
these only in the fields named in the index.

### p7

bash entrypoint, broad exec_paths, git denials -- PATH form denied with the reason; ABSOLUTE path bypasses

```json
{
  "meta": {
    "name": "git deny probe"
  },
  "filesystem": {
    "allow": [
      "$WORKDIR"
    ],
    "read": [
      "/nix/store",
      "/run/current-system/sw"
    ]
  },
  "environment": {
    "allow_vars": [
      "PATH",
      "HOME",
      "USER",
      "LANG",
      "TERM"
    ]
  },
  "command_policies": {
    "commands": {
      "bash": {
        "executable": "/run/current-system/sw/bin/bash",
        "can_use": [
          "git"
        ],
        "from": {
          "session": {
            "sandbox": {
              "fs_read_file": [
                "/run/current-system/sw/bin/bash"
              ],
              "fs_read": [
                "/nix/store",
                "/run/current-system/sw",
                "$WORKDIR"
              ],
              "fs_write": [
                "$WORKDIR"
              ],
              "environment": {
                "allow_vars": [
                  "PATH",
                  "HOME",
                  "USER",
                  "LANG",
                  "TERM"
                ]
              },
              "exec_paths": [
                "/nix/store",
                "/run/current-system/sw"
              ]
            }
          }
        }
      },
      "git": {
        "executable": "/nix/store/8yh8zbb0r4na7gkqk0db0gbdid2ax7zm-git-2.54.0/bin/git",
        "from": {
          "bash": {
            "sandbox": {
              "fs_read_file": [
                "/nix/store/8yh8zbb0r4na7gkqk0db0gbdid2ax7zm-git-2.54.0/bin/git"
              ],
              "fs_read": [
                "/nix/store",
                "/run/current-system/sw"
              ],
              "fs_write": [
                "$WORKDIR"
              ],
              "environment": {
                "allow_vars": [
                  "PATH",
                  "HOME",
                  "USER",
                  "LANG",
                  "TERM"
                ]
              }
            },
            "invocation_policy": {
              "deny": [
                {
                  "argv": {
                    "contains": [
                      "--force"
                    ]
                  },
                  "reason": "force push is disabled in this sandbox"
                },
                {
                  "argv": {
                    "prefix": [
                      "reset",
                      "--hard"
                    ]
                  },
                  "reason": "hard reset is disabled in this sandbox"
                }
              ],
              "default": "allow"
            }
          }
        }
      }
    }
  }
}
```

### p8

git as the session entrypoint, no bash anywhere -- denial enforced; absolute path refused as direct exec bypass denied

```json
{
  "meta": {
    "name": "git as session entrypoint"
  },
  "filesystem": {
    "allow": [
      "$WORKDIR"
    ],
    "read": [
      "/nix/store",
      "/run/current-system/sw"
    ]
  },
  "environment": {
    "allow_vars": [
      "PATH",
      "HOME",
      "USER",
      "LANG",
      "TERM"
    ]
  },
  "command_policies": {
    "commands": {
      "git": {
        "executable": "/nix/store/8yh8zbb0r4na7gkqk0db0gbdid2ax7zm-git-2.54.0/bin/git",
        "from": {
          "session": {
            "sandbox": {
              "fs_read_file": [
                "/nix/store/8yh8zbb0r4na7gkqk0db0gbdid2ax7zm-git-2.54.0/bin/git"
              ],
              "fs_read": [
                "/nix/store",
                "/run/current-system/sw"
              ],
              "fs_write": [
                "$WORKDIR"
              ],
              "exec_paths": [
                "/nix/store",
                "/run/current-system/sw"
              ],
              "environment": {
                "allow_vars": [
                  "PATH",
                  "HOME",
                  "USER",
                  "LANG",
                  "TERM"
                ]
              }
            },
            "invocation_policy": {
              "default": "allow",
              "deny": [
                {
                  "argv": {
                    "prefix": [
                      "reset",
                      "--hard"
                    ]
                  },
                  "reason": "hard reset is disabled in this sandbox"
                }
              ]
            }
          }
        }
      }
    }
  }
}
```

### n12

bash child allow_all, curl child allow_domain = [pypi] only -- pypi 200 AND github 200 - a child's allow_domain is ignored

```json
{
  "meta": {
    "name": "git deny probe"
  },
  "filesystem": {
    "allow": [
      "$WORKDIR"
    ],
    "read": [
      "/nix/store",
      "/run/current-system/sw"
    ]
  },
  "environment": {
    "allow_vars": [
      "PATH",
      "HOME",
      "USER",
      "LANG",
      "TERM"
    ]
  },
  "command_policies": {
    "commands": {
      "bash": {
        "executable": "/run/current-system/sw/bin/bash",
        "can_use": [
          "git",
          "curl"
        ],
        "from": {
          "session": {
            "sandbox": {
              "fs_read_file": [
                "/run/current-system/sw/bin/bash"
              ],
              "fs_read": [
                "/nix/store",
                "/run/current-system/sw",
                "$WORKDIR",
                "/etc/ssl",
                "/etc/pki"
              ],
              "fs_write": [
                "$WORKDIR"
              ],
              "environment": {
                "allow_vars": [
                  "*"
                ]
              },
              "exec_paths": [
                "/nix/store",
                "/run/current-system/sw"
              ],
              "network": {
                "allow_all": true
              }
            }
          }
        }
      },
      "git": {
        "executable": "/nix/store/8yh8zbb0r4na7gkqk0db0gbdid2ax7zm-git-2.54.0/bin/git",
        "from": {
          "bash": {
            "sandbox": {
              "fs_read_file": [
                "/nix/store/8yh8zbb0r4na7gkqk0db0gbdid2ax7zm-git-2.54.0/bin/git"
              ],
              "fs_read": [
                "/nix/store",
                "/run/current-system/sw"
              ],
              "fs_write": [
                "$WORKDIR"
              ],
              "environment": {
                "allow_vars": [
                  "PATH",
                  "HOME",
                  "USER",
                  "LANG",
                  "TERM"
                ]
              }
            },
            "invocation_policy": {
              "deny": [
                {
                  "argv": {
                    "contains": [
                      "--force"
                    ]
                  },
                  "reason": "force push is disabled in this sandbox"
                },
                {
                  "argv": {
                    "prefix": [
                      "reset",
                      "--hard"
                    ]
                  },
                  "reason": "hard reset is disabled in this sandbox"
                }
              ],
              "default": "allow"
            }
          }
        }
      },
      "curl": {
        "executable": "/nix/store/82knz13wjnsgpihsfaywrhpy6prjks6v-curl-8.21.0-bin/bin/curl",
        "from": {
          "bash": {
            "sandbox": {
              "fs_read_file": [
                "/nix/store/82knz13wjnsgpihsfaywrhpy6prjks6v-curl-8.21.0-bin/bin/curl"
              ],
              "fs_read": [
                "/nix/store",
                "/run/current-system/sw",
                "/etc/ssl",
                "/etc/pki"
              ],
              "exec_paths": [
                "/nix/store",
                "/run/current-system/sw"
              ],
              "network": {
                "allow_domain": [
                  "pypi.org"
                ]
              },
              "environment": {
                "allow_vars": [
                  "*"
                ]
              }
            }
          }
        }
      }
    }
  },
  "network": {
    "network_profile": "developer"
  }
}
```

### s1

curl as the session entrypoint with network.allow_all, session = developer -- pypi 200, example.com 000 - per-command on/off under a session ceiling

```json
{
  "meta": {
    "name": "curl as session entrypoint"
  },
  "filesystem": {
    "allow": [
      "$WORKDIR"
    ],
    "read": [
      "/nix/store",
      "/run/current-system/sw",
      "/etc/ssl",
      "/etc/pki"
    ]
  },
  "environment": {
    "allow_vars": [
      "*"
    ]
  },
  "network": {
    "network_profile": "developer"
  },
  "command_policies": {
    "commands": {
      "curl": {
        "executable": "/nix/store/82knz13wjnsgpihsfaywrhpy6prjks6v-curl-8.21.0-bin/bin/curl",
        "from": {
          "session": {
            "sandbox": {
              "fs_read_file": [
                "/nix/store/82knz13wjnsgpihsfaywrhpy6prjks6v-curl-8.21.0-bin/bin/curl"
              ],
              "fs_read": [
                "/nix/store",
                "/run/current-system/sw",
                "/etc/ssl",
                "/etc/pki"
              ],
              "network": {
                "allow_all": true
              },
              "environment": {
                "allow_vars": [
                  "*"
                ]
              }
            }
          }
        }
      }
    }
  }
}
```

### b1

broker stand-in as a policy command, NO exec_paths -- reaches its can_use shims; absolute path, symlink, non-policy commands and bash all refused at execve

```json
{
  "meta": {
    "name": "broker inside nono run"
  },
  "filesystem": {
    "allow": [
      "$WORKDIR"
    ],
    "read": [
      "/nix/store",
      "/run/current-system/sw"
    ],
    "read_file": [
      "/home/yn/.cache/asb-probe/fakebroker"
    ]
  },
  "environment": {
    "allow_vars": [
      "*"
    ]
  },
  "command_policies": {
    "commands": {
      "fakebroker": {
        "executable": "/home/yn/.cache/asb-probe/fakebroker",
        "can_use": [
          "git"
        ],
        "from": {
          "session": {
            "sandbox": {
              "fs_read_file": [
                "/home/yn/.cache/asb-probe/fakebroker"
              ],
              "fs_read": [
                "/nix/store",
                "/run/current-system/sw",
                "$WORKDIR"
              ],
              "fs_write": [
                "$WORKDIR"
              ],
              "environment": {
                "allow_vars": [
                  "*"
                ]
              }
            }
          }
        }
      },
      "git": {
        "executable": "/nix/store/8yh8zbb0r4na7gkqk0db0gbdid2ax7zm-git-2.54.0/bin/git",
        "from": {
          "fakebroker": {
            "sandbox": {
              "fs_read_file": [
                "/nix/store/8yh8zbb0r4na7gkqk0db0gbdid2ax7zm-git-2.54.0/bin/git"
              ],
              "fs_read": [
                "/nix/store",
                "/run/current-system/sw"
              ],
              "fs_write": [
                "$WORKDIR"
              ],
              "environment": {
                "allow_vars": [
                  "*"
                ]
              }
            },
            "invocation_policy": {
              "default": "allow",
              "deny": [
                {
                  "argv": {
                    "prefix": [
                      "reset",
                      "--hard"
                    ]
                  },
                  "reason": "hard reset is disabled in this sandbox"
                }
              ]
            }
          }
        }
      }
    },
    "executable_dirs": [
      "/home/yn/.cache/asb-probe"
    ]
  }
}
```

### b2

same with exec_paths = [git's real binary] -- absolute path bypasses - exec_paths is precisely the hole

```json
{
  "meta": {
    "name": "broker inside nono run"
  },
  "filesystem": {
    "allow": [
      "$WORKDIR"
    ],
    "read": [
      "/nix/store",
      "/run/current-system/sw"
    ],
    "read_file": [
      "/home/yn/.cache/asb-probe/fakebroker"
    ]
  },
  "environment": {
    "allow_vars": [
      "*"
    ]
  },
  "command_policies": {
    "commands": {
      "fakebroker": {
        "executable": "/home/yn/.cache/asb-probe/fakebroker",
        "can_use": [
          "git"
        ],
        "from": {
          "session": {
            "sandbox": {
              "fs_read_file": [
                "/home/yn/.cache/asb-probe/fakebroker"
              ],
              "fs_read": [
                "/nix/store",
                "/run/current-system/sw",
                "$WORKDIR"
              ],
              "fs_write": [
                "$WORKDIR"
              ],
              "environment": {
                "allow_vars": [
                  "*"
                ]
              },
              "exec_paths": [
                "/nix/store/8yh8zbb0r4na7gkqk0db0gbdid2ax7zm-git-2.54.0/bin/git"
              ]
            }
          }
        }
      },
      "git": {
        "executable": "/nix/store/8yh8zbb0r4na7gkqk0db0gbdid2ax7zm-git-2.54.0/bin/git",
        "from": {
          "fakebroker": {
            "sandbox": {
              "fs_read_file": [
                "/nix/store/8yh8zbb0r4na7gkqk0db0gbdid2ax7zm-git-2.54.0/bin/git"
              ],
              "fs_read": [
                "/nix/store",
                "/run/current-system/sw"
              ],
              "fs_write": [
                "$WORKDIR"
              ],
              "environment": {
                "allow_vars": [
                  "*"
                ]
              }
            },
            "invocation_policy": {
              "default": "allow",
              "deny": [
                {
                  "argv": {
                    "prefix": [
                      "reset",
                      "--hard"
                    ]
                  },
                  "reason": "hard reset is disabled in this sandbox"
                }
              ]
            }
          }
        }
      }
    },
    "executable_dirs": [
      "/home/yn/.cache/asb-probe"
    ]
  }
}
```

### sh1

broker interpreting the line with mvdan.cc/sh; git and rg both policy commands -- shell language works; git deny enforced; policy-to-policy pipeline hangs

```json
{
  "meta": {
    "name": "shell-interpreting broker"
  },
  "filesystem": {
    "allow": [
      "$WORKDIR"
    ],
    "read": [
      "/nix/store",
      "/run/current-system/sw"
    ],
    "read_file": [
      "/home/yn/.cache/asb-probe/shbroker"
    ]
  },
  "environment": {
    "allow_vars": [
      "*"
    ]
  },
  "command_policies": {
    "executable_dirs": [
      "/home/yn/.cache/asb-probe"
    ],
    "commands": {
      "shbroker": {
        "executable": "/home/yn/.cache/asb-probe/shbroker",
        "can_use": [
          "git",
          "rg"
        ],
        "from": {
          "session": {
            "sandbox": {
              "fs_read_file": [
                "/home/yn/.cache/asb-probe/shbroker"
              ],
              "fs_read": [
                "/nix/store",
                "/run/current-system/sw",
                "$WORKDIR"
              ],
              "fs_write": [
                "$WORKDIR"
              ],
              "environment": {
                "allow_vars": [
                  "*"
                ]
              }
            }
          }
        }
      },
      "git": {
        "executable": "/nix/store/8yh8zbb0r4na7gkqk0db0gbdid2ax7zm-git-2.54.0/bin/git",
        "from": {
          "shbroker": {
            "sandbox": {
              "fs_read_file": [
                "/nix/store/8yh8zbb0r4na7gkqk0db0gbdid2ax7zm-git-2.54.0/bin/git"
              ],
              "fs_read": [
                "/nix/store",
                "/run/current-system/sw",
                "$WORKDIR"
              ],
              "fs_write": [
                "$WORKDIR"
              ],
              "environment": {
                "allow_vars": [
                  "*"
                ]
              }
            },
            "invocation_policy": {
              "default": "allow",
              "deny": [
                {
                  "argv": {
                    "prefix": [
                      "reset",
                      "--hard"
                    ]
                  },
                  "reason": "hard reset is disabled in this sandbox"
                }
              ]
            }
          }
        }
      },
      "rg": {
        "executable": "/nix/store/zc6fy62c341aibk36p4ywff7rf9wn4wx-ripgrep-15.2.0/bin/rg",
        "from": {
          "shbroker": {
            "sandbox": {
              "fs_read_file": [
                "/nix/store/zc6fy62c341aibk36p4ywff7rf9wn4wx-ripgrep-15.2.0/bin/rg"
              ],
              "fs_read": [
                "/nix/store",
                "/run/current-system/sw",
                "$WORKDIR"
              ],
              "fs_write": [
                "$WORKDIR"
              ],
              "environment": {
                "allow_vars": [
                  "*"
                ]
              }
            }
          }
        }
      }
    }
  }
}
```

### sh2

same, but rg moved to the broker's exec_paths instead of being a policy command -- pipeline works, git deny still unbypassable, bash still refused

```json
{
  "meta": {
    "name": "policy for git only; rest run on the broker floor"
  },
  "filesystem": {
    "allow": [
      "$WORKDIR"
    ],
    "read": [
      "/nix/store",
      "/run/current-system/sw"
    ],
    "read_file": [
      "/home/yn/.cache/asb-probe/shbroker"
    ]
  },
  "environment": {
    "allow_vars": [
      "*"
    ]
  },
  "command_policies": {
    "executable_dirs": [
      "/home/yn/.cache/asb-probe"
    ],
    "commands": {
      "shbroker": {
        "executable": "/home/yn/.cache/asb-probe/shbroker",
        "can_use": [
          "git"
        ],
        "from": {
          "session": {
            "sandbox": {
              "fs_read_file": [
                "/home/yn/.cache/asb-probe/shbroker"
              ],
              "fs_read": [
                "/nix/store",
                "/run/current-system/sw",
                "$WORKDIR"
              ],
              "fs_write": [
                "$WORKDIR"
              ],
              "exec_paths": [
                "/nix/store/zc6fy62c341aibk36p4ywff7rf9wn4wx-ripgrep-15.2.0/bin"
              ],
              "environment": {
                "allow_vars": [
                  "*"
                ]
              }
            }
          }
        }
      },
      "git": {
        "executable": "/nix/store/8yh8zbb0r4na7gkqk0db0gbdid2ax7zm-git-2.54.0/bin/git",
        "from": {
          "shbroker": {
            "sandbox": {
              "fs_read_file": [
                "/nix/store/8yh8zbb0r4na7gkqk0db0gbdid2ax7zm-git-2.54.0/bin/git"
              ],
              "fs_read": [
                "/nix/store",
                "/run/current-system/sw"
              ],
              "fs_write": [
                "$WORKDIR"
              ],
              "environment": {
                "allow_vars": [
                  "*"
                ]
              }
            },
            "invocation_policy": {
              "default": "allow",
              "deny": [
                {
                  "argv": {
                    "prefix": [
                      "reset",
                      "--hard"
                    ]
                  },
                  "reason": "hard reset is disabled in this sandbox"
                }
              ]
            }
          }
        }
      }
    }
  }
}
```

### dp1

`cmd/doctor.go`'s `checkToolSandbox` probe profile, exactly as committed --
OK on both working builds; the broken build fails immediately with nono's own
ELF-resolution error, before the probe command ever runs.

Added retroactively (task 7 review, ruling R19): `checkToolSandbox`'s job is
to tell a broken nono from a working one, so its discrimination needs to be
reproducible from this file the same way every other measurement here is, not
resting on narrative in a code comment. This is the exact JSON
`writeToolSandboxProbeProfile` emits (`agent-sandbox` substituted for the
temp dir and the resolved `true` binary at write time); `$WORKDIR` above is
literal in every other profile in this file, but here the field really does
hold a concrete absolute path, because the probe never expects nono to
expand anything — it grants exactly the one temp directory it just created.

```json
{
  "meta": {
    "name": "agent-sandbox doctor tool-sandbox probe"
  },
  "groups": {
    "include": [
      "nix_runtime"
    ]
  },
  "filesystem": {
    "allow": [
      "<probe's own temp dir>"
    ]
  },
  "environment": {
    "allow_vars": [
      "PATH"
    ]
  },
  "command_policies": {
    "commands": {
      "true": {
        "executable": "<resolved path of `true` on PATH>",
        "from": {
          "session": {
            "sandbox": {
              "fs_read_file": [
                "<resolved path of `true` on PATH>"
              ],
              "environment": {
                "allow_vars": [
                  "PATH"
                ]
              }
            }
          }
        }
      }
    }
  }
}
```

Run as `nono run --silent --profile <file> --workdir <temp dir> -- true`
(the bare command name, not the resolved absolute path — see below).

**Why `groups.include: ["nix_runtime"]` and nothing else.** Before landing on
this shape, the same profile with no `groups` key at all (only the command's
own `fs_read_file` on the `true` binary) failed even against this host's
*working* nono, with `exit code 127` and nono's own diagnostic *"'true'
resolved to /tmp/nono-tool-sandbox-.../shims/true and is readable, but
execution still failed"*. That failure happens in the *outer* session,
before the probe's own per-command sandbox grants ever matter: whenever
`command_policies` is non-empty, nono scans every non-writable `PATH`
directory to decide what its generated shim may execute, and on NixOS that
scan has no route to `/nix/store` without help. Granting `/nix/store` and
`/run/current-system/sw` directly worked, but is NixOS-specific and would be
silently useless on an FHS host or macOS. Granting `filesystem.allow: ["/"]`
was refused outright: `nono: Sandbox initialization failed: Refusing to grant
'/' (source: Profile) because it overlaps protected nono state root
'~/.local/state/nono'`. `nix_runtime` is one of nono's own built-in policy
groups (`nono profile groups nix_runtime`; "Platform: cross-platform",
"Required: no") that grants `/nix/store`, `/run/current-system/sw`, and a few
`~/.nix-*` paths for read, and is a documented no-op where those paths don't
exist — which is what makes this profile portable rather than NixOS-only.

**Why the bare command name (`-- true`), not an absolute path.** Invoking the
same pinned policy command by its resolved absolute path instead of its bare
name hits a *different* nono refusal: `nono: Command '<path>' is blocked:
tool-sandbox direct exec bypass denied for policy-controlled command 'true'`.
That is nono correctly treating a direct-path invocation of a policy command
as bypassing its own shim, not a tool-sandbox startup failure — using it
would have made the probe report NG against a perfectly working nono.

**Measured 2026-09-05** with `nono run --silent --profile <file> --workdir
<dir> -- true`, reproduced fresh for this entry (not only recalled from
earlier exploration) against three builds:

| build | provenance | result |
| --- | --- | --- |
| this host's real nono, `nono 0.74.0` at `~/.local/bin/nono` | the nono this whole branch has been developed and measured against | `exit 0` |
| a separately built `nono 0.75.0` with `tmp/nono-nixos-elf-fix.patch` applied, built at `.../nono-src/target/release/nono` from a checked-out `always-further/nono` source tree (a prior session's scratchpad; not part of this repo) | patched per the fix this design already assumes is a prerequisite | `exit 0` |
| a musl-statically-linked `nono 0.74.0` build, found already built in the same prior session's scratchpad (exact build flags/provenance not recorded — its own dependency-closure computation happens to hit the same unresolved-`libgcc_s.so.1`-symlink shape the NixOS ELF bug describes, whether or not it carries the fix) | build history not verified beyond what its own failure shows; treat "unpatched" as observed, not confirmed | `exit 1`, immediately, with `nono: Sandbox initialization failed: failed to resolve ELF dependency 'libc.so.6' for /nix/store/avld9cdn23zab2ssl30h2r6444rqh6ms-glibc-2.42-67/lib/libdl.so.2` — a session-startup failure, not specific to the probe command, matching what the design doc's own NixOS ELF section predicts for any dependency closure that runs through the affected symlink |

The third build's failure is the shape `checkToolSandbox`'s hint names
("the nono on PATH cannot start tool-sandbox on this host ... a common cause
is an unpatched nono on NixOS, which cannot resolve its ELF dependency
layout"): it fires before the profile's own `command_policies.commands.true`
entry is ever reached, so it is a property of the nono binary itself, not of
how the probe's own command is configured — which is exactly what the check
needs to catch.
