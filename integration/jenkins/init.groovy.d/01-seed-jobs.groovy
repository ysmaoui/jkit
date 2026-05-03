import jenkins.model.*
import hudson.model.*
import org.jenkinsci.plugins.workflow.job.*
import org.jenkinsci.plugins.workflow.cps.*
import com.cloudbees.hudson.plugins.folder.*

def jenkins = Jenkins.get()

// 1. test-job: simple freestyle
def testJob = jenkins.createProject(FreeStyleProject, 'test-job')
testJob.buildersList.add(new hudson.tasks.Shell("echo 'Hello from test-job'; sleep 2; echo 'Done'"))

// 2. test-pipeline: 3-stage pipeline
def pipeline = jenkins.createProject(WorkflowJob, 'test-pipeline')
pipeline.setDefinition(new CpsFlowDefinition("""\
pipeline {
    agent any
    stages {
        stage('Build') {
            steps {
                echo 'Building...'
                sleep 1
            }
        }
        stage('Test') {
            steps {
                echo 'Testing...'
                sleep 1
            }
        }
        stage('Deploy') {
            steps {
                echo 'Deploying...'
                sleep 1
            }
        }
    }
}
""", true))

// 3. test-folder with inner-job
def folder = jenkins.createProject(Folder, 'test-folder')
def innerJob = folder.createProject(FreeStyleProject, 'inner-job')
innerJob.buildersList.add(new hudson.tasks.Shell("echo 'Inside test-folder'"))

// 4. param-job: freestyle with parameters
def paramJob = jenkins.createProject(FreeStyleProject, 'param-job')
def branchParam = new hudson.model.StringParameterDefinition('BRANCH', 'main', 'Branch name')
def envParam = new hudson.model.StringParameterDefinition('ENV', 'dev', 'Environment')
paramJob.addProperty(new hudson.model.ParametersDefinitionProperty(branchParam, envParam))
paramJob.buildersList.add(new hudson.tasks.Shell('echo "BRANCH=$BRANCH ENV=$ENV"'))

jenkins.save()
println '>>> Seed jobs created successfully'
