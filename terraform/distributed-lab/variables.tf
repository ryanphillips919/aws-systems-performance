variable "region" {
  type    = string
  default = "us-east-1"
}
variable "availability_zone" {
  type        = string
  description = "Availability zone in the selected region"
}
variable "key_name" {
  type        = string
  description = "Existing EC2 key pair name"
}
variable "operator_cidr" {
  type        = string
  description = "Operator public IPv4 address as a /32"
  validation {
    condition     = can(cidrhost(var.operator_cidr, 0)) && endswith(var.operator_cidr, "/32") && !strcontains(var.operator_cidr, ":")
    error_message = "operator_cidr must be an IPv4 /32."
  }
}
variable "ami_id" {
  type        = string
  default     = ""
  description = "Optional pinned AL2023 x86_64 AMI"
}
variable "infra_instance_type" {
  type    = string
  default = "t3.medium"
}
variable "api_instance_type" {
  type    = string
  default = "t3.medium"
}
variable "db_instance_type" {
  type    = string
  default = "t3.small"
}
