mock_provider "aws" {
  mock_data "aws_availability_zones" {
    defaults = { names = ["us-east-1a", "us-east-1b"] }
  }
  mock_data "aws_ami" {
    defaults = { id = "ami-0123456789abcdef0" }
  }
}
mock_provider "random" {}
variables {
  allowed_cidr = "203.0.113.10/32"
}
run "baseline" {
  command = plan
  assert {
    condition     = length(aws_instance.app) == 2 && alltrue([for instance in aws_instance.app : !instance.associate_public_ip_address])
    error_message = "Two application instances must have no public addresses."
  }
  assert {
    condition     = !aws_db_instance.db.publicly_accessible && aws_db_instance.db.storage_encrypted && aws_db_instance.db.manage_master_user_password
    error_message = "RDS must be private and encrypted with an RDS-managed password."
  }
  assert {
    condition     = length(aws_elasticache_replication_group.redis) == 0 && aws_lb_target_group.app.health_check[0].path == "/health/serving"
    error_message = "Redis should be opt-in and ALB should check service availability."
  }
}
run "with_redis" {
  command = plan
  variables { enable_redis = true }
  assert {
    condition     = length(aws_elasticache_replication_group.redis) == 1 && aws_elasticache_replication_group.redis[0].transit_encryption_enabled && aws_elasticache_replication_group.redis[0].at_rest_encryption_enabled
    error_message = "Optional Redis must be encrypted at rest and in transit."
  }
}
