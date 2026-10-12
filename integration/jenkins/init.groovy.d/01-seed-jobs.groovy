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

// 5. export-pipeline: the shapes `jkit export html` renders. Snapshot exports
// this same run from inside it while After is still pending; run.sh copies
// the jkit binary into the container before the first build.
def exportPipeline = jenkins.createProject(WorkflowJob, 'export-pipeline')
exportPipeline.setDefinition(new CpsFlowDefinition('''\
pipeline {
    agent any
    options { timestamps() }
    stages {
        stage('Build') {
            steps {
                echo 'Building...'
                sh 'echo build-output'
                ansiColor('xterm') {
                    echo '\u001B[31mred text\u001B[0m plain'
                }
            }
        }
        stage('Long Log') {
            steps {
                sh 'seq 1 20000'
            }
        }
        stage('Flaky') {
            steps {
                catchError(buildResult: 'SUCCESS', stageResult: 'FAILURE') {
                    sh 'echo about to fail; exit 3'
                }
            }
        }
        stage('Fan Out') {
            parallel {
                stage('Branch A') {
                    steps {
                        echo 'in branch A'
                    }
                }
                stage('Branch B') {
                    stages {
                        stage('B1') {
                            steps {
                                echo 'in B1'
                            }
                        }
                        stage('B2') {
                            steps {
                                echo 'in B2'
                            }
                        }
                    }
                }
            }
        }
        stage('Snapshot') {
            steps {
                sh \'\'\'
                    export JKIT_CONFIG_DIR="$WORKSPACE/.jkit"
                    jkit auth login --host http://localhost:8080 --user admin --token admin
                    jkit export html -o snapshot.html
                \'\'\'
                archiveArtifacts 'snapshot.html'
            }
        }
        stage('After') {
            steps {
                echo 'after the snapshot'
            }
        }
    }
    post {
        always {
            echo 'post always'
        }
    }
}
''', true))

jenkins.save()
println '>>> Seed jobs created successfully'
