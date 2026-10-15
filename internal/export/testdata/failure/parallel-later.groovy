pipeline {
  agent any
  stages {
    stage('P') {
      parallel {
        stage('X') {
          stages {
            stage('X1') { steps { sleep 1 } }
            stage('X2') { steps { catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') { error 'caught' } } }
          }
        }
        stage('Y') {
          stages {
            stage('Y1') { steps { sleep 4; error 'real' } }
          }
        }
      }
    }
  }
}
