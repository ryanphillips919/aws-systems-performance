variable "region" {
  description = "AWS region. Review the estimate for this region before deployment."
  type        = string
  default     = "us-east-1"
}
variable "name" {
  description = "Short resource prefix unique within the account and region."
  type        = string
  default     = "urlperf"
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,14}$", var.name))
    error_message = "Use 2–15 lowercase letters, numbers or hyphens, starting with a letter."
  }
}
variable "allowed_cidr" {
  description = "Trusted client IPv4 CIDR for the HTTP listener, preferably your public IP /32."
  type        = string
  validation {
    condition     = can(cidrnetmask(var.allowed_cidr)) && var.allowed_cidr != "0.0.0.0/0"
    error_message = "Provide a restricted IPv4 CIDR, not 0.0.0.0/0."
  }
}
variable "enable_redis" {
  description = "Create a single private ElastiCache Redis node."
  type        = bool
  default     = false
}
variable "instance_type" {
  type    = string
  default = "t3.micro"
}
variable "db_instance_class" {
  type    = string
  default = "db.t3.micro"
}
variable "redis_node_type" {
  type    = string
  default = "cache.t3.micro"
}

variable "ami_id" {
  description = "Optional pinned x86_64 Amazon Linux 2023 AMI ID. Null selects the latest official image."
  type        = string
  default     = null
  validation {
    condition     = var.ami_id == null ? true : can(regex("^ami-[0-9a-f]+$", var.ami_id))
    error_message = "ami_id must be an AMI ID or null."
  }
}
