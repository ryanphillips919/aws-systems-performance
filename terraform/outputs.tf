output "base_url" { value = "http://${aws_lb.app.dns_name}" }
output "alb_dns_name" { value = aws_lb.app.dns_name }
output "target_group_arn" { value = aws_lb_target_group.app.arn }
output "instance_ids" { value = aws_instance.app[*].id }
output "database_identifier" { value = aws_db_instance.db.identifier }
output "artifact_bucket" { value = aws_s3_bucket.artifact.id }
output "redis_enabled" { value = var.enable_redis }
