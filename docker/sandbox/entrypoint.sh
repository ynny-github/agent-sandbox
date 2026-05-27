#!/bin/bash
echo "Container started."
echo "Keeping container alive..."

exec tail -f /dev/null
