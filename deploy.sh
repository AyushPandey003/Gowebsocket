#!/bin/bash

# First-time deployment script for EC2
# Run this script on your EC2 instance to set up the application

set -e

echo "=== Starting first-time deployment setup ==="

# Variables - Update these according to your setup
APP_NAME="gowebsocket"
APP_DIR="/home/ec2-user/$APP_NAME"
REPO_URL="https://github.com/AyushPandey003/Gowebsocket.git"
SERVICE_USER="ec2-user"
GO_VERSION="1.23.2"

# Install Go if not already installed
if ! command -v go &> /dev/null; then
    echo "Installing Go..."
    wget https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf go${GO_VERSION}.linux-amd64.tar.gz
    echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
    export PATH=$PATH:/usr/local/go/bin
    rm go${GO_VERSION}.linux-amd64.tar.gz
    echo "Go installed successfully"
else
    echo "Go is already installed: $(go version)"
fi

# Install Git if not already installed
if ! command -v git &> /dev/null; then
    echo "Installing Git..."
    sudo yum update -y
    sudo yum install -y git
fi

# Clone repository
if [ ! -d "$APP_DIR" ]; then
    echo "Cloning repository..."
    git clone $REPO_URL $APP_DIR
else
    echo "Repository already exists, pulling latest changes..."
    cd $APP_DIR
    git pull origin main
fi

# Navigate to app directory
cd $APP_DIR

# Install dependencies
echo "Installing Go dependencies..."
go mod download

# Build the application
echo "Building the application..."
go build -o server ./cmd/server

# Make the binary executable
chmod +x server

# Create systemd service file
echo "Creating systemd service..."
sudo tee /etc/systemd/system/${APP_NAME}.service > /dev/null <<EOF
[Unit]
Description=GoWebSocket Server
After=network.target

[Service]
Type=simple
User=$SERVICE_USER
WorkingDirectory=$APP_DIR
ExecStart=$APP_DIR/server
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=$APP_NAME

# Environment variables (add your variables here)
Environment="ENV=production"

[Install]
WantedBy=multi-user.target
EOF

# Reload systemd daemon
echo "Reloading systemd daemon..."
sudo systemctl daemon-reload

# Enable service to start on boot
echo "Enabling service to start on boot..."
sudo systemctl enable ${APP_NAME}.service

# Start the service
echo "Starting the service..."
sudo systemctl start ${APP_NAME}.service

# Check service status
echo "Checking service status..."
sudo systemctl status ${APP_NAME}.service --no-pager

echo ""
echo "=== Deployment complete! ==="
echo ""
echo "Useful commands:"
echo "  Check status:  sudo systemctl status ${APP_NAME}.service"
echo "  View logs:     sudo journalctl -u ${APP_NAME}.service -f"
echo "  Restart:       sudo systemctl restart ${APP_NAME}.service"
echo "  Stop:          sudo systemctl stop ${APP_NAME}.service"
echo ""
