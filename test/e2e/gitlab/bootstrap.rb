project_path = ENV.fetch("E2E_GITLAB_PROJECT_PATH")
project_namespace, project_name = project_path.split("/", 2)
raise "E2E_GITLAB_PROJECT_PATH must include namespace/project" if project_name.nil? || project_name.empty?

root = User.find_by_username!("root")
namespace = Namespace.find_by_full_path(project_namespace)
raise "namespace #{project_namespace.inspect} not found" if namespace.nil?

project = Project.find_by_full_path(project_path)
unless project
  project = Projects::CreateService.new(
    root,
    name: project_name,
    path: project_name,
    namespace_id: namespace.id,
    visibility_level: Gitlab::VisibilityLevel::PRIVATE
  ).execute
  raise "project create failed: #{project.errors.full_messages.join(", ")}" unless project.persisted?
end

token_value = ENV.fetch("E2E_GITLAB_TOKEN")
token_name = "openbao-secret-sync-e2e"
expires_at = 30.days.from_now.to_date
token = root.personal_access_tokens.active.find_by(name: token_name)
token ||= root.personal_access_tokens.build(name: token_name, scopes: [:api], expires_at: expires_at)
token.expires_at = expires_at
token.scopes = [:api]
token.set_token(token_value)
token.save!

puts "GitLab e2e bootstrap complete for #{project.full_path}"
