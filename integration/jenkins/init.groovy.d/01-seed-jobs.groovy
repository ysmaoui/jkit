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

// slow-pipeline: holds an executor through node{} long enough to be seen running
def slowPipeline = folder.createProject(WorkflowJob, 'slow-pipeline')
slowPipeline.setDefinition(new CpsFlowDefinition("node { sleep 300 }", true))

// 4. param-job: freestyle with parameters
def paramJob = jenkins.createProject(FreeStyleProject, 'param-job')
def branchParam = new hudson.model.StringParameterDefinition('BRANCH', 'main', 'Branch name')
def envParam = new hudson.model.StringParameterDefinition('ENV', 'dev', 'Environment')
paramJob.addProperty(new hudson.model.ParametersDefinitionProperty(branchParam, envParam))
paramJob.buildersList.add(new hudson.tasks.Shell('echo "BRANCH=$BRANCH ENV=$ENV"'))

// 5. export-pipeline: the shapes `jkit export html` renders. Snapshot exports
// this same run from inside it while After is still pending; run.sh copies
// the jkit binary into the container before the first build. That export's
// annotated console stops at the line cap inside Long Log, so the echo and
// sh steps before it, Race's included, come from the console and the rest
// are read one by one. Race ends before the cut: its steps are proved by
// the nodes after them within their branches, not by being cut.
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
        stage('Race') {
            parallel {
                stage('Race A') {
                    steps {
                        sh 'echo a1; sleep 8; echo a2'
                    }
                }
                stage('Race B') {
                    steps {
                        sleep 1
                        echo 'b'
                    }
                }
                stage('Race C') {
                    steps {
                        sleep 4
                        echo 'c'
                    }
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
                    jkit export html -o snapshot.html 2>snapshot.err
                    jkit export html --log-source steps -o snapshot-steps.html
                \'\'\'
                archiveArtifacts 'snapshot.html, snapshot.err, snapshot-steps.html'
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

// 6. console-pipeline: the console shapes the progressiveHtml parser fixtures
// in internal/export/testdata come from. writeFile prints nothing.
def consolePipeline = jenkins.createProject(WorkflowJob, 'console-pipeline')
consolePipeline.setDefinition(new CpsFlowDefinition('''\
pipeline {
    agent any
    options { timestamps() }
    stages {
        stage('Text') {
            steps {
                echo 'a < b && c > "d" \\'e\\''
                sh 'printf "two\\\\nlines\\\\n"; printf "no newline at end"'
                ansiColor('xterm') {
                    echo '\u001B[31mred text\u001B[0m plain'
                }
                writeFile file: 'quiet.txt', text: 'x'
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
                        sh 'for i in 1 2 3; do echo A$i; sleep 1; done'
                    }
                }
                stage('Branch B') {
                    stages {
                        stage('B1') {
                            steps {
                                sh 'for i in 1 2 3; do echo B$i; sleep 1; done'
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
    }
    post {
        always {
            echo 'post always'
        }
    }
}
''', true))

// 7. compile-error-pipeline: fails to compile, so the build ends before any
// flow node exists.
def compileErrorPipeline = jenkins.createProject(WorkflowJob, 'compile-error-pipeline')
compileErrorPipeline.setDefinition(new CpsFlowDefinition('''\
pipeline {
    agent any
    stages {
        stage('A') {
            steps {
                echo 'unclosed'
''', true))

jenkins.save()
println '>>> Seed jobs created successfully'
