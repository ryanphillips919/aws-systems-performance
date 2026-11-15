terraform {
  required_version = ">= 1.7.0"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 5.0" }
  }
}
provider "aws" { region = var.region }

data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]
  filter {
    name   = "name"
    values = ["al2023-ami-2023*-x86_64"]
  }
  filter {
    name   = "state"
    values = ["available"]
  }
}
resource "aws_vpc" "lab" {
  cidr_block           = "10.42.0.0/24"
  enable_dns_hostnames = true
  enable_dns_support   = true
  tags                 = { Name = "distributed-benchmark" }
}
resource "aws_subnet" "lab" {
  vpc_id                  = aws_vpc.lab.id
  cidr_block              = aws_vpc.lab.cidr_block
  availability_zone       = var.availability_zone
  map_public_ip_on_launch = true
}
resource "aws_internet_gateway" "lab" { vpc_id = aws_vpc.lab.id }
resource "aws_route_table" "lab" {
  vpc_id = aws_vpc.lab.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.lab.id
  }
}
resource "aws_route_table_association" "lab" {
  subnet_id      = aws_subnet.lab.id
  route_table_id = aws_route_table.lab.id
}
resource "aws_security_group" "host" {
  for_each    = toset(["infra", "api", "db"])
  name_prefix = "distributed-benchmark-${each.key}-"
  vpc_id      = aws_vpc.lab.id
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  lifecycle { create_before_destroy = true }
}
resource "aws_vpc_security_group_ingress_rule" "ssh" {
  for_each          = aws_security_group.host
  security_group_id = each.value.id
  cidr_ipv4         = var.operator_cidr
  ip_protocol       = "tcp"
  from_port         = 22
  to_port           = 22
}
resource "aws_vpc_security_group_ingress_rule" "api" {
  security_group_id            = aws_security_group.host["api"].id
  referenced_security_group_id = aws_security_group.host["infra"].id
  ip_protocol                  = "tcp"
  from_port                    = 8000
  to_port                      = 8000
}
resource "aws_vpc_security_group_ingress_rule" "db" {
  security_group_id            = aws_security_group.host["db"].id
  referenced_security_group_id = aws_security_group.host["api"].id
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
}
resource "aws_vpc_security_group_ingress_rule" "redis" {
  security_group_id            = aws_security_group.host["infra"].id
  referenced_security_group_id = aws_security_group.host["api"].id
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
}
locals {
  hosts = {
    infra  = { ip = "10.42.0.10", role = "infra", type = var.infra_instance_type }
    api1   = { ip = "10.42.0.11", role = "api", type = var.api_instance_type }
    api2   = { ip = "10.42.0.12", role = "api", type = var.api_instance_type }
    shard1 = { ip = "10.42.0.13", role = "db", type = var.db_instance_type }
    shard2 = { ip = "10.42.0.14", role = "db", type = var.db_instance_type }
    shard3 = { ip = "10.42.0.15", role = "db", type = var.db_instance_type }
  }
}
resource "aws_instance" "host" {
  for_each                    = local.hosts
  ami                         = var.ami_id != "" ? var.ami_id : data.aws_ami.al2023.id
  instance_type               = each.value.type
  subnet_id                   = aws_subnet.lab.id
  private_ip                  = each.value.ip
  associate_public_ip_address = true
  key_name                    = var.key_name
  vpc_security_group_ids      = [aws_security_group.host[each.value.role].id]
  metadata_options { http_tokens = "required" }
  root_block_device {
    volume_size           = 20
    volume_type           = "gp3"
    encrypted             = true
    delete_on_termination = true
  }
  user_data = file("${path.module}/bootstrap.sh")
  tags      = { Name = "distributed-benchmark-${each.key}", Project = "distributed-benchmark" }
}
output "public_ips" { value = { for name, host in aws_instance.host : name => host.public_ip } }
output "private_ips" { value = { for name, host in aws_instance.host : name => host.private_ip } }
